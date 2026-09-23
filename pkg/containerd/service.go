//go:build linux && cgo

package containerd

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/cmd/containerd/command"
	"github.com/eclipse-iofog/edgelet/internal/cgroups"
	"github.com/eclipse-iofog/edgelet/internal/constants"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/eclipse-iofog/edgelet/pkg/data"
)

const maxRetries = 30

const (
	containerdStartupTimeout      = 60 * time.Second
	containerdClientRetryInterval = 2 * time.Second
	containerdChildArg            = "--edgelet-containerd-child"
)

const (
	reconfigureStageWriteConfig     = "write_config"
	reconfigureStageStopRuntime     = "stop_runtime"
	reconfigureStageStartRuntime    = "start_runtime"
	reconfigureStageWaitCRIReady    = "wait_cri_ready"
	reconfigureStageVerifyStability = "verify_stability"
	reconfigureStageRollbackConfig  = "rollback_config"
	reconfigureStageEscalateRestart = "escalate_restart"
	reconfigureStageDone            = "done"
)

var containerdShutdownWaitTimeout = 15 * time.Second
var shimReapRemainingBudget = DefaultShimReapBudgetCap
var containerdReconfigureStabilityWindow = 3 * time.Second
var containerdReconfigureRetryDelay = 500 * time.Millisecond
var containerdReconfigureMaxAttempts = 2

const runtimeV2TaskDirName = "io.containerd.runtime.v2.task"

const (
	staleTaskCleanupMaxPasses  = 6
	staleTaskCleanupRetryDelay = 2 * time.Second
)

var (
	containerdStateDirForCleanup   = constants.EdgeletContainerdStateDir
	runtimeArtifactCleanupSocket   = constants.EdgeletContainerdSocket
	runtimeArtifactCleanupRunDir   = constants.EdgeletRunDir
	runtimeArtifactCleanupStateDir = constants.EdgeletContainerdStateDir
	removeStaleRuntimeTaskDir      = os.RemoveAll
	staleTaskCleanupSleep          = time.Sleep
)
var resolveChildExecutable = data.RuntimeBinary
var writeConfigForService = writeConfigFile
var readLKGForService = readLastKnownGoodConfig
var writeAtomicForService = writeFileAtomically
var signalSelfForService = func(sig syscall.Signal) error { return syscall.Kill(os.Getpid(), sig) }
var sleepForReconfigure = time.Sleep

var (
	ErrContainerdSpawnFailure   = errors.New("containerd spawn failure")
	ErrContainerdReadiness      = errors.New("containerd readiness failure")
	ErrContainerdExitedEarly    = errors.New("containerd exited before ready")
	ErrContainerdStopTimeout    = errors.New("containerd stop timeout")
	ErrContainerdUnexpectedExit = errors.New("containerd unexpected exit")
	ErrContainerdReconfigure    = errors.New("containerd reconfigure failure")
)

var logger = logging.NewModuleLogger("Containerd")

// Service manages the embedded containerd child-process lifecycle.
type Service struct {
	mu sync.Mutex

	ctx    context.Context
	cancel context.CancelFunc

	cmd     *exec.Cmd
	waitErr error

	ready     chan struct{}
	readyOnce sync.Once
	done      chan struct{}

	runFn func() error

	unexpectedExitHandler func(error)
	attachOnly            bool
}

type ErrContainerdReconfigureOperation struct {
	Stage   string
	Attempt int
	Err     error
}

func (e *ErrContainerdReconfigureOperation) Error() string {
	if e == nil || e.Err == nil {
		return "containerd reconfigure operation failed"
	}
	return e.Err.Error()
}

func (e *ErrContainerdReconfigureOperation) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *ErrContainerdReconfigureOperation) Details() map[string]any {
	stage := strings.TrimSpace(strings.ToLower(e.Stage))
	if stage == "" {
		return nil
	}
	details := map[string]any{
		"stage": stage,
	}
	if e.Attempt > 0 {
		details["attempt"] = e.Attempt
	}
	return details
}

// NewAttachedService monitors an externally managed containerd (Plan 11 data-plane unit).
func NewAttachedService() *Service {
	ctx, cancel := context.WithCancel(context.Background())
	svc := &Service{
		ctx:        ctx,
		cancel:     cancel,
		ready:      make(chan struct{}),
		done:       make(chan struct{}),
		attachOnly: true,
	}
	return svc
}

// IsAttachOnly reports whether this service only monitors an external containerd process.
func (s *Service) IsAttachOnly() bool {
	if s == nil {
		return false
	}
	return s.attachOnly
}

// NewService creates an uninitialized containerd service.
func NewService() *Service {
	ctx, cancel := context.WithCancel(context.Background())
	svc := &Service{
		ctx:    ctx,
		cancel: cancel,
		ready:  make(chan struct{}),
		done:   make(chan struct{}),
	}
	svc.runFn = svc.Run
	return svc
}

// MaybeRunChildProcess runs containerd in child mode when the daemon is invoked
// with the dedicated internal child argument.
func MaybeRunChildProcess(args []string) (bool, error) {
	if len(args) < 2 || args[1] != containerdChildArg {
		return false, nil
	}
	return true, runContainerdChild(args[2:])
}

func runContainerdChild(args []string) error {
	app := command.App()
	app.Flags = buildFlags()
	argv := append([]string{"containerd-child"}, args...)
	return app.Run(argv)
}

// Run starts containerd as a managed child process and blocks until it exits.
func (s *Service) Run() error {
	defer close(s.done)

	logger.Infof("Starting embedded containerd child process (socket=%s config=%s)",
		constants.EdgeletContainerdSocket, constants.EdgeletContainerdConfigFile)

	if err := s.prepare(); err != nil {
		return fmt.Errorf("%w: prepare runtime directories: %w", ErrContainerdSpawnFailure, err)
	}

	if policy := cgroups.GetGlobalPolicy(); policy != nil {
		if err := cgroups.ValidatePreflight(policy); err != nil {
			return fmt.Errorf("%w: cgroup preflight: %w", ErrContainerdSpawnFailure, err)
		}
	}

	if err := writeConfigForService(); err != nil {
		return fmt.Errorf("%w: write config: %w", ErrContainerdSpawnFailure, err)
	}

	cmd, err := s.spawnChild()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrContainerdSpawnFailure, err)
	}

	processExitCh := make(chan error, 1)
	go func() {
		waitErr := cmd.Wait()
		clearLiveContainerdChildPID(cmd.Process.Pid)
		processExitCh <- waitErr
	}()

	startupErrCh := make(chan error, 1)
	go func() {
		startupErrCh <- s.postSetup()
	}()

	ready := false
	var runErr error

	for {
		select {
		case err := <-startupErrCh:
			startupErrCh = nil
			if err != nil {
				runErr = fmt.Errorf("%w: %w", ErrContainerdReadiness, err)
				logger.Errorf("Embedded containerd readiness failed: %v", err)
				logStaleTaskMetadataHintIfPresent(err)
				s.cancel()
				_ = s.signalProcess(syscall.SIGTERM)
			} else {
				ready = true
			}
		case err := <-processExitCh:
			s.mu.Lock()
			s.waitErr = err
			s.mu.Unlock()

			if runErr != nil {
				return runErr
			}
			if !ready {
				if err != nil {
					return fmt.Errorf("%w: %w", ErrContainerdExitedEarly, err)
				}
				return ErrContainerdExitedEarly
			}
			if s.ctx.Err() != nil {
				if err != nil {
					logger.Debugf("Containerd child exited during shutdown: %v", err)
				}
				return nil
			}
			if err != nil {
				s.notifyUnexpectedExit(fmt.Errorf("%w: %w", ErrContainerdUnexpectedExit, err))
				return fmt.Errorf("%w: %w", ErrContainerdUnexpectedExit, err)
			}
			s.notifyUnexpectedExit(ErrContainerdUnexpectedExit)
			return ErrContainerdUnexpectedExit
		case <-s.ctx.Done():
			logger.Info("Containerd service stopping.")
			if err := s.signalProcess(syscall.SIGTERM); err != nil {
				logger.Warnf("Failed to signal containerd child with SIGTERM: %v", err)
			}

			select {
			case err := <-processExitCh:
				s.mu.Lock()
				s.waitErr = err
				s.mu.Unlock()
				if err != nil {
					logger.Debugf("Containerd child exited after stop signal: %v", err)
				}
				return nil
			case <-time.After(containerdShutdownWaitTimeout):
				logger.Warnf("Timed out waiting for containerd child to stop after %s; forcing kill", containerdShutdownWaitTimeout)
				if killErr := s.signalProcess(syscall.SIGKILL); killErr != nil {
					logger.Warnf("Failed to force-kill containerd child: %v", killErr)
				}
				select {
				case err := <-processExitCh:
					s.mu.Lock()
					s.waitErr = err
					s.mu.Unlock()
					if err != nil {
						logger.Debugf("Containerd child exited after SIGKILL: %v", err)
					}
				case <-time.After(containerdShutdownWaitTimeout):
					return fmt.Errorf("%w: exceeded forced stop wait window", ErrContainerdStopTimeout)
				}
				return nil
			}
		}
	}
}

// Reconfigure rewrites containerd config and performs a controlled restart.
func (s *Service) Reconfigure() error {
	previousLKG, lkgErr := readLKGForService(constants.EdgeletContainerdConfigFile)
	hasLKG := lkgErr == nil && len(previousLKG) > 0

	if err := writeConfigForService(); err != nil {
		return fmt.Errorf("%w: %w", ErrContainerdReconfigure, wrapReconfigureError(reconfigureStageWriteConfig, 0, fmt.Errorf("write config: %w", err)))
	}

	var lastErr error
	for attempt := 1; attempt <= containerdReconfigureMaxAttempts; attempt++ {
		err := s.reconfigureAttempt(attempt)
		if err == nil {
			return nil
		}
		lastErr = err
		logger.Warnf("Embedded containerd reconfigure attempt %d/%d failed: %v", attempt, containerdReconfigureMaxAttempts, err)
		if attempt < containerdReconfigureMaxAttempts {
			sleepForReconfigure(containerdReconfigureRetryDelay)
		}
	}

	rollbackErr := s.rollbackToLKG(previousLKG, hasLKG)
	if rollbackErr == nil {
		return fmt.Errorf(
			"%w: %w",
			ErrContainerdReconfigure,
			wrapReconfigureError(
				reconfigureStageRollbackConfig,
				0,
				fmt.Errorf("reconfigure failed and rollback to last-known-good config succeeded: %w", lastErr),
			),
		)
	}
	lastErr = fmt.Errorf("%w; rollback failed: %w", lastErr, rollbackErr)
	if err := s.escalateRestart(lastErr); err != nil {
		return fmt.Errorf("%w: %w", ErrContainerdReconfigure, err)
	}
	return fmt.Errorf("%w: %w", ErrContainerdReconfigure, wrapReconfigureError(reconfigureStageEscalateRestart, 0, lastErr))
}

// Ready returns a channel that closes when containerd is ready to accept requests.
func (s *Service) Ready() <-chan struct{} {
	return s.ready
}

// Start launches the service in background and waits for readiness.
func (s *Service) Start() error {
	if s.attachOnly {
		return s.startAttached()
	}
	errCh := make(chan error, 1)
	run := s.runFn
	if run == nil {
		run = s.Run
	}
	go func() {
		errCh <- run()
	}()

	select {
	case <-s.ready:
		return nil
	case err := <-errCh:
		return describeStartupFailure(err)
	}
}

func describeStartupFailure(err error) error {
	if err == nil {
		return ErrContainerdExitedEarly
	}
	switch {
	case errors.Is(err, ErrContainerdSpawnFailure):
		return fmt.Errorf("spawn embedded containerd child: %w", err)
	case errors.Is(err, ErrContainerdReadiness):
		return fmt.Errorf("wait for embedded containerd readiness: %w", err)
	case errors.Is(err, ErrContainerdExitedEarly):
		return err
	default:
		return fmt.Errorf("embedded containerd run loop failed: %w", err)
	}
}

func (s *Service) startAttached() error {
	deadline := time.Now().Add(containerdStartupTimeout)
	for time.Now().Before(deadline) {
		if s.IsHealthy() {
			s.readyOnce.Do(func() { close(s.ready) })
			logger.Info("Attached to externally managed containerd socket")
			return nil
		}
		time.Sleep(containerdClientRetryInterval)
	}
	return fmt.Errorf("%w: containerd socket not ready for attach", ErrContainerdReadiness)
}

// WaitReady waits for the service to become ready up to timeout.
func (s *Service) WaitReady(timeout time.Duration) error {
	select {
	case <-s.ready:
		return nil
	case <-s.done:
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.waitErr != nil {
			return fmt.Errorf("%w: %w", ErrContainerdExitedEarly, s.waitErr)
		}
		return ErrContainerdExitedEarly
	case <-time.After(timeout):
		return fmt.Errorf("%w: timed out after %s", ErrContainerdReadiness, timeout)
	}
}

// StopGraceful requests a graceful shutdown and waits for completion.
func (s *Service) StopGraceful() error {
	s.cancel()
	_ = s.signalProcess(syscall.SIGTERM)
	if !waitForDone(s.done, containerdShutdownWaitTimeout) {
		return fmt.Errorf("%w: graceful stop exceeded %s", ErrContainerdStopTimeout, containerdShutdownWaitTimeout)
	}
	return s.reapManagedShims()
}

// StopForce force-kills the child process and waits for completion.
func (s *Service) StopForce() error {
	s.cancel()
	_ = s.signalProcess(syscall.SIGKILL)
	if !waitForDone(s.done, containerdShutdownWaitTimeout) {
		return fmt.Errorf("%w: force stop exceeded %s", ErrContainerdStopTimeout, containerdShutdownWaitTimeout)
	}
	return s.reapManagedShims()
}

func (s *Service) resetForRestart() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.cmd = nil
	s.waitErr = nil
	s.ready = make(chan struct{})
	s.readyOnce = sync.Once{}
	s.done = make(chan struct{})
	if s.runFn == nil {
		s.runFn = s.Run
	}
}

// Reap waits for the service goroutine to finish.
func (s *Service) Reap() error {
	if !waitForDone(s.done, containerdShutdownWaitTimeout) {
		return fmt.Errorf("%w: reap exceeded %s", ErrContainerdStopTimeout, containerdShutdownWaitTimeout)
	}
	return nil
}

// Stop keeps backward-compatible behavior while enforcing escalation.
func (s *Service) Stop() {
	if s.attachOnly {
		return
	}
	err := s.StopGraceful()
	if err == nil {
		return
	}
	logger.Warnf("Graceful containerd stop failed: %v", err)
	if err := s.StopForce(); err != nil {
		logger.Warnf("Forced containerd stop failed: %v", err)
	}
}

func (s *Service) SetUnexpectedExitHandler(handler func(error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unexpectedExitHandler = handler
}

// IsHealthy returns true when containerd is reachable and the CRI runtime plugin is usable.
func (s *Service) IsHealthy() bool {
	c, err := client.New(constants.EdgeletContainerdSocket)
	if err != nil {
		return false
	}
	defer func() {
		_ = c.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), criHealthProbeTimeout)
	defer cancel()
	if _, err = c.Version(ctx); err != nil {
		return false
	}
	return criRuntimeUsable()
}

func (s *Service) spawnChild() (*exec.Cmd, error) {
	execPath, err := resolveChildExecutable()
	if err != nil {
		return nil, fmt.Errorf("resolve fat runtime executable: %w", err)
	}

	args := []string{
		containerdChildArg,
		"--config", constants.EdgeletContainerdConfigFile,
		"--address", constants.EdgeletContainerdSocket,
		"--root", constants.EdgeletContainerdRootDir,
		"--state", constants.EdgeletContainerdStateDir,
		"--log-level", "warn",
	}

	cmd := exec.Command(execPath, args...) // #nosec G204 -- fixed executable path + controlled args
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start child process: %w", err)
	}
	setLiveContainerdChildPID(cmd.Process.Pid)

	s.mu.Lock()
	s.cmd = cmd
	s.mu.Unlock()

	logger.Infof("Embedded containerd child started (pid=%d)", cmd.Process.Pid)
	return cmd, nil
}

func (s *Service) signalProcess(sig syscall.Signal) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	pid := s.cmd.Process.Pid
	if pid <= 0 {
		return nil
	}
	if err := syscall.Kill(-pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("signal process group %d with %s: %w", pid, sig, err)
	}
	return nil
}

func (s *Service) notifyUnexpectedExit(err error) {
	s.mu.Lock()
	handler := s.unexpectedExitHandler
	s.mu.Unlock()
	if handler == nil {
		return
	}
	go handler(err)
}

func wrapReconfigureError(stage string, attempt int, err error) error {
	if err == nil {
		return nil
	}
	return &ErrContainerdReconfigureOperation{
		Stage:   stage,
		Attempt: attempt,
		Err:     err,
	}
}

func (s *Service) reconfigureAttempt(attempt int) error {
	if err := s.StopGraceful(); err != nil {
		logger.Warnf("Graceful stop during reconfigure attempt %d failed: %v", attempt, err)
		if forceErr := s.StopForce(); forceErr != nil {
			return wrapReconfigureError(reconfigureStageStopRuntime, attempt, fmt.Errorf("stop for reconfigure failed (graceful: %w, force: %w)", err, forceErr))
		}
	}
	s.resetForRestart()
	if err := s.Start(); err != nil {
		return wrapReconfigureError(reconfigureStageStartRuntime, attempt, fmt.Errorf("restart failed: %w", err))
	}
	if err := s.WaitReady(containerdStartupTimeout); err != nil {
		return wrapReconfigureError(reconfigureStageWaitCRIReady, attempt, err)
	}
	if err := s.verifyStabilityWindow(containerdReconfigureStabilityWindow); err != nil {
		return wrapReconfigureError(reconfigureStageVerifyStability, attempt, err)
	}
	return nil
}

func (s *Service) verifyStabilityWindow(window time.Duration) error {
	deadline := time.Now().Add(window)
	for {
		select {
		case <-s.done:
			s.mu.Lock()
			waitErr := s.waitErr
			s.mu.Unlock()
			if waitErr != nil {
				return fmt.Errorf("containerd exited during stability window: %w", waitErr)
			}
			return errors.New("containerd exited during stability window")
		default:
		}
		if time.Now().After(deadline) {
			return nil
		}
		sleepForReconfigure(200 * time.Millisecond)
	}
}

func (s *Service) rollbackToLKG(previousLKG []byte, hasLKG bool) error {
	if !hasLKG {
		return wrapReconfigureError(reconfigureStageRollbackConfig, 0, errors.New("last-known-good config is unavailable"))
	}
	if err := writeAtomicForService(constants.EdgeletContainerdConfigFile, previousLKG, 0644); err != nil {
		return wrapReconfigureError(reconfigureStageRollbackConfig, 0, fmt.Errorf("restore last-known-good config: %w", err))
	}
	if err := s.reconfigureAttempt(0); err != nil {
		return wrapReconfigureError(reconfigureStageRollbackConfig, 0, fmt.Errorf("restart after rollback failed: %w", err))
	}
	return nil
}

func (s *Service) escalateRestart(cause error) error {
	if err := signalSelfForService(syscall.SIGTERM); err != nil {
		return wrapReconfigureError(reconfigureStageEscalateRestart, 0, fmt.Errorf("failed to signal daemon for restart (cause: %w): %w", cause, err))
	}
	return wrapReconfigureError(reconfigureStageEscalateRestart, 0, fmt.Errorf("requested daemon restart after persistent reconfigure failure: %w", cause))
}

// prepare ensures required directories exist and removes any stale socket
// from a previous run (e.g. after crash) so containerd can bind successfully.
func (s *Service) prepare() error {
	_ = os.Remove(constants.EdgeletContainerdSocket)

	for _, dir := range []string{
		constants.EdgeletContainerdRootDir,
		constants.EdgeletContainerdStateDir,
		constants.EdgeletRunDir,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil { // #nosec G301 -- runtime dirs under /run/edgelet require containerd socket access
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	return nil
}

// postSetup waits for containerd health, then creates the iofog namespace.
func (s *Service) postSetup() error {
	ctx, cancel := context.WithTimeout(s.ctx, containerdStartupTimeout)
	defer cancel()

	c, err := s.waitForClient(ctx)
	if err != nil {
		return fmt.Errorf("create containerd client: %w", err)
	}
	defer func() {
		_ = c.Close()
	}()

	if err := s.healthCheck(ctx, c); err != nil {
		return fmt.Errorf("health check: %w", err)
	}

	if err := ensureNamespace(ctx, c); err != nil {
		return fmt.Errorf("ensure namespace: %w", err)
	}

	logger.Info("Embedded containerd is ready.")
	s.readyOnce.Do(func() {
		close(s.ready)
	})
	return nil
}

// healthCheck retries a Version call until containerd responds or times out.
func (s *Service) healthCheck(ctx context.Context, c *client.Client) error {
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		_, err := c.Version(ctx)
		if err == nil {
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return fmt.Errorf("health check timed out: %w", lastErr)
		case <-time.After(2 * time.Second):
		}
	}
	if lastErr != nil {
		return fmt.Errorf("containerd did not become healthy after %d retries: %w", maxRetries, lastErr)
	}
	return fmt.Errorf("containerd did not become healthy after %d retries", maxRetries)
}

func (s *Service) waitForClient(ctx context.Context) (*client.Client, error) {
	var lastErr error
	for {
		c, err := client.New(constants.EdgeletContainerdSocket)
		if err == nil {
			return c, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("timed out waiting for containerd socket %s: %w", constants.EdgeletContainerdSocket, lastErr)
		case <-time.After(containerdClientRetryInterval):
		}
	}
}

func waitForDone(done <-chan struct{}, timeout time.Duration) bool {
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// RemainingStopBudget returns the stop time left from totalBudget since stopStarted.
func RemainingStopBudget(stopStarted time.Time, totalBudget time.Duration) time.Duration {
	remaining := totalBudget - time.Since(stopStarted)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// SetShimReapRemainingBudget configures the shim-reap slice for the next Stop/reap call.
func SetShimReapRemainingBudget(remaining time.Duration) {
	if remaining < 0 {
		remaining = 0
	}
	shimReapRemainingBudget = remaining
}

// ResetShimReapRemainingBudget restores the default shim-reap budget cap.
func ResetShimReapRemainingBudget() {
	shimReapRemainingBudget = DefaultShimReapBudgetCap
}

func (s *Service) reapManagedShims() error {
	return reapManagedShimsForSocket(constants.EdgeletContainerdSocket, shimReapRemainingBudget)
}

// StopOrphanedEmbeddedContainerd stops leftover embedded containerd children from /proc.
func StopOrphanedEmbeddedContainerd() error {
	return stopOrphanedEmbeddedContainerdFromProc()
}

type staleTaskCleanupResult struct {
	ebusyDirs []string
	fatalErr  error
}

func isRemoveAllEBUSY(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EBUSY) {
		return true
	}
	// Match kernel errno text only; avoid substring "ebusy" (false positives in paths
	// such as ...ReturnsFatalOnNonEBUSY... in test temp dirs).
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "device or resource busy")
}

func cleanupStaleRuntimeTasksPass() staleTaskCleanupResult {
	base := filepath.Join(containerdStateDirForCleanup, runtimeV2TaskDirName)
	return cleanupStaleRuntimeTasksInDir(base)
}

func cleanupStaleRuntimeTasksInDir(base string) staleTaskCleanupResult {
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return staleTaskCleanupResult{}
		}
		return staleTaskCleanupResult{fatalErr: fmt.Errorf("read runtime task directory %s: %w", base, err)}
	}

	var errs []string
	var ebusyDirs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		taskDir := filepath.Join(base, entry.Name())
		if entry.Name() == constants.EdgeletContainerdNamespace {
			nested := cleanupStaleRuntimeTasksInDir(taskDir)
			if nested.fatalErr != nil {
				errs = append(errs, nested.fatalErr.Error())
			}
			ebusyDirs = append(ebusyDirs, nested.ebusyDirs...)
			continue
		}
		addressPath := filepath.Join(taskDir, "address")
		_, statErr := os.Stat(addressPath)
		if statErr == nil {
			continue
		}
		if os.IsNotExist(statErr) {
			if hasNestedRuntimeTaskDirs(taskDir) {
				nested := cleanupStaleRuntimeTasksInDir(taskDir)
				if nested.fatalErr != nil {
					errs = append(errs, nested.fatalErr.Error())
				}
				ebusyDirs = append(ebusyDirs, nested.ebusyDirs...)
				continue
			}
			logger.Warnf("Removing stale runtime task directory without address file: %s", taskDir)
		} else {
			logger.Warnf("Removing stale runtime task directory with unreadable address file %s: %v", addressPath, statErr)
		}
		if prepErr := prepareStaleRuntimeTaskDirRemoval(taskDir); prepErr != nil {
			logger.Warnf("Stale runtime task pre-removal failed for %s: %v", taskDir, prepErr)
		}
		if removeErr := removeStaleRuntimeTaskDir(taskDir); removeErr != nil && !os.IsNotExist(removeErr) {
			if isRemoveAllEBUSY(removeErr) {
				logger.Warnf("Stale runtime task directory still busy; will retry after shim reap: %s", taskDir)
				ebusyDirs = append(ebusyDirs, taskDir)
				continue
			}
			errs = append(errs, fmt.Sprintf("%s: %v", taskDir, removeErr))
			continue
		}
		logger.Infof("Removed stale runtime task directory: %s", taskDir)
	}

	if len(errs) > 0 {
		return staleTaskCleanupResult{fatalErr: fmt.Errorf("stale runtime task cleanup failed: %s", strings.Join(errs, "; "))}
	}
	return staleTaskCleanupResult{ebusyDirs: ebusyDirs}
}

func hasNestedRuntimeTaskDirs(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		child := filepath.Join(dir, entry.Name())
		if _, err := os.Stat(filepath.Join(child, "address")); err == nil {
			return true
		}
		if _, err := os.Stat(filepath.Join(child, "rootfs")); err == nil {
			return true
		}
		if _, err := os.Stat(filepath.Join(child, "config.json")); err == nil {
			return true
		}
	}
	return false
}

func prepareStaleRuntimeTaskDirRemoval(taskDir string) error {
	taskID := filepath.Base(taskDir)
	if err := ReapShimsForStaleTask(constants.EdgeletContainerdSocket, taskID, DefaultShimReapBudgetCap); err != nil {
		return err
	}
	return detachStaleRuntimeTaskMounts(taskDir)
}

func detachStaleRuntimeTaskMounts(taskDir string) error {
	taskDir = filepath.Clean(taskDir)
	mounts, err := mountPointsUnder(taskDir)
	if err != nil {
		return err
	}
	var errs []string
	for _, mountPoint := range mounts {
		if umountErr := syscall.Unmount(mountPoint, syscall.MNT_DETACH); umountErr != nil && !errors.Is(umountErr, syscall.EINVAL) && !errors.Is(umountErr, syscall.ENOENT) {
			errs = append(errs, fmt.Sprintf("%s: %v", mountPoint, umountErr))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("detach stale runtime task mounts: %s", strings.Join(errs, "; "))
	}
	return nil
}

func mountPointsUnder(root string) ([]string, error) {
	root = filepath.Clean(root)
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, fmt.Errorf("read mountinfo: %w", err)
	}

	mounts := make([]string, 0)
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		mountPoint := fields[4]
		mountPoint = strings.ReplaceAll(mountPoint, "\\040", " ")
		if mountPoint == root || strings.HasPrefix(mountPoint, root+"/") {
			mounts = append(mounts, mountPoint)
		}
	}
	slices.SortFunc(mounts, func(a, b string) int {
		return cmp.Compare(len(b), len(a))
	})
	return mounts, nil
}

// CleanupStaleRuntimeTasks removes orphaned runtime task directories under
// EdgeletContainerdStateDir. Preserves EdgeletContainerdRootDir (images/layers).
func CleanupStaleRuntimeTasks() error {
	res := cleanupStaleRuntimeTasksPass()
	if res.fatalErr != nil {
		return res.fatalErr
	}
	if len(res.ebusyDirs) > 0 {
		return fmt.Errorf("stale runtime task cleanup blocked by EBUSY: %s", strings.Join(res.ebusyDirs, "; "))
	}
	return nil
}

// CleanupStaleRuntimeTasksWithRetry removes stale runtime task directories with bounded
// EBUSY retries after shim reap. Bootstrap continues when dirs remain busy.
func CleanupStaleRuntimeTasksWithRetry() error {
	var lastEBUSY []string
	for pass := 1; pass <= staleTaskCleanupMaxPasses; pass++ {
		res := cleanupStaleRuntimeTasksPass()
		if res.fatalErr != nil {
			return res.fatalErr
		}
		if len(res.ebusyDirs) == 0 {
			return nil
		}
		lastEBUSY = res.ebusyDirs
		logger.Warnf(
			"stale runtime task cleanup pass %d/%d: %d director(y/ies) still EBUSY",
			pass, staleTaskCleanupMaxPasses, len(res.ebusyDirs),
		)
		if pass < staleTaskCleanupMaxPasses {
			if err := ReapManagedShimsUntilClear(constants.EdgeletContainerdSocket, DefaultShimReapBudgetCap); err != nil {
				logger.Warnf("managed shim reap during stale task cleanup retry failed: %v", err)
			}
			staleTaskCleanupSleep(staleTaskCleanupRetryDelay)
		}
	}
	if len(lastEBUSY) > 0 {
		logger.Warnf(
			"stale runtime task cleanup incomplete after %d passes; continuing bootstrap: %s",
			staleTaskCleanupMaxPasses, strings.Join(lastEBUSY, "; "),
		)
	}
	return nil
}

// PrepareEmbeddedContainerdBootstrap stops orphans, reaps shims, and cleans stale tasks
// before embedded containerd start or retry.
func PrepareEmbeddedContainerdBootstrap() {
	if err := stopOrphanedEmbeddedContainerdFromProc(); err != nil {
		logger.Warnf("orphan embedded containerd stop before bootstrap failed: %v", err)
	}
	if err := ReapManagedShimsUntilClear(constants.EdgeletContainerdSocket, DefaultShimReapBudgetCap); err != nil {
		logger.Warnf("managed shim reap before bootstrap incomplete: %v", err)
	}
	if err := CleanupStaleRuntimeTasksWithRetry(); err != nil {
		logger.Warnf("stale runtime task cleanup before bootstrap failed: %v", err)
	}
}

func logStaleTaskMetadataHintIfPresent(err error) {
	if err == nil {
		return
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "shim") || strings.Contains(msg, "address") {
		logger.Warn("containerd reported stale shim/task metadata; stale task cleanup runs on next data-plane bootstrap")
	}
}

// CleanupRuntimeArtifacts removes stale runtime artifacts used by embedded containerd.
// It intentionally preserves persistent image/layer data under EdgeletContainerdRootDir.
func CleanupRuntimeArtifacts() error {
	if err := stopOrphanedEmbeddedContainerdFromProc(); err != nil {
		return fmt.Errorf("stop orphaned embedded containerd children: %w", err)
	}

	var errs []string
	if err := os.Remove(runtimeArtifactCleanupSocket); err != nil && !os.IsNotExist(err) {
		errs = append(errs, fmt.Sprintf("%s: %v", runtimeArtifactCleanupSocket, err))
	}
	// Keep /run/edgelet itself. Removing it unlinks the control-plane socket
	// while the daemon is still running.
	if err := os.RemoveAll(runtimeArtifactCleanupStateDir); err != nil && !os.IsNotExist(err) {
		errs = append(errs, fmt.Sprintf("%s: %v", runtimeArtifactCleanupStateDir, err))
	}

	for _, dir := range []string{runtimeArtifactCleanupRunDir, runtimeArtifactCleanupStateDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil { // #nosec G301 -- runtime dirs under /run/edgelet require containerd socket access
			errs = append(errs, fmt.Sprintf("mkdir %s: %v", dir, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("cleanup runtime artifacts failed: %s", strings.Join(errs, "; "))
	}
	return nil
}
