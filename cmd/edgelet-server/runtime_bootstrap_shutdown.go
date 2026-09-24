//go:build linux && cgo

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/constants"
	"github.com/eclipse-iofog/edgelet/internal/processmanager"
	"github.com/eclipse-iofog/edgelet/internal/utils"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/eclipse-iofog/edgelet/pkg/containerd"
	"github.com/eclipse-iofog/edgelet/pkg/engine/edgelet/cri"
)

const (
	edgeletAPISocketWaitBudget = 45 * time.Second
	edgeletAPISocketPoll       = 500 * time.Millisecond
)

var errStopBudgetExhausted = errors.New("data-plane stop budget exhausted")

// bootstrapTestWaitForAPISocket, when set, overrides waitForEdgeletAPISocket (tests only).
var bootstrapTestWaitForAPISocket func(time.Duration) bool

// bootstrapTestNewCRIRuntime, when set, supplies the CRI runtime used by data-plane drain (tests only).
var bootstrapTestNewCRIRuntime func() (cri.WorkloadRuntime, func(), error)

// bootstrapTestVolumeHolders, when set, overrides the volume-tree holder scan (tests only).
var bootstrapTestVolumeHolders func(string) ([]int, error)

type dataPlaneDrainOutcome struct {
	complete     bool
	timedOut     bool
	degraded     bool
	verifyFailed bool
}

func (o dataPlaneDrainOutcome) status() string {
	switch {
	case o.verifyFailed:
		return processmanager.DrainQuiesceVerifyFailed
	case o.complete:
		return processmanager.DrainQuiesceComplete
	case o.timedOut:
		return processmanager.DrainQuiesceTimedOut
	case o.degraded:
		return processmanager.DrainQuiesceTimedOut
	default:
		return processmanager.DrainQuiesceTimedOut
	}
}

func (o dataPlaneDrainOutcome) allowsShimReap() bool {
	return o.complete && !o.verifyFailed && !o.timedOut && !o.degraded
}

type runtimeBootstrapShutdownDeps struct {
	shouldDrain func() bool
	drain       func(drainSec int) dataPlaneDrainOutcome
}

func defaultRuntimeBootstrapShutdownDeps() runtimeBootstrapShutdownDeps {
	return runtimeBootstrapShutdownDeps{
		shouldDrain: shouldDrainOnDataPlaneSIGTERM,
		drain:       quiesceDataPlaneViaCRI,
	}
}

// shouldDrainOnDataPlaneSIGTERM reports whether runtime-bootstrap should invoke
// coordinated MS drain before stopping embedded containerd (runtime split only).
func shouldDrainOnDataPlaneSIGTERM() bool {
	if os.Getenv("EDGELET_RUNTIME_SPLIT") == "0" {
		return false
	}
	if logging.RuntimeSplitFromEnv() {
		return true
	}
	// runtime-bootstrap runs on edgelet-containerd.service (split data plane).
	return true
}

func runDataPlaneDrainBeforeStop(drainSec int, deps runtimeBootstrapShutdownDeps) dataPlaneDrainOutcome {
	if deps.shouldDrain != nil && !deps.shouldDrain() {
		return dataPlaneDrainOutcome{complete: true}
	}
	if deps.drain == nil {
		deps.drain = quiesceDataPlaneViaCRI
	}

	logging.LogInfo("RUNTIME_BOOTSTRAP", "drain_started")
	outcome := deps.drain(drainSec)
	switch {
	case outcome.complete:
		logging.LogInfo("RUNTIME_BOOTSTRAP", "drain_complete")
	case outcome.verifyFailed:
		logging.LogError("RUNTIME_BOOTSTRAP", "drain_verify_failed", errDrainVerifyFailed)
	case outcome.timedOut:
		logging.LogWarn("RUNTIME_BOOTSTRAP", "drain_timeout")
	default:
		logging.LogWarn("RUNTIME_BOOTSTRAP", "drain_degraded")
	}
	return outcome
}

var errDrainVerifyFailed = errors.New("data-plane drain verify failed")

func edgeletAPISocketCandidates() []string {
	return []string{
		filepath.Join(utils.VarRun, "edgelet.sock"),
		filepath.Join(constants.EdgeletRunDir, "edgelet.sock"),
	}
}

func waitForEdgeletAPISocket(budget time.Duration) bool {
	if bootstrapTestWaitForAPISocket != nil {
		return bootstrapTestWaitForAPISocket(budget)
	}
	if budget <= 0 {
		budget = edgeletAPISocketWaitBudget
	}
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		for _, path := range edgeletAPISocketCandidates() {
			if _, err := os.Stat(path); err == nil {
				return true
			}
		}
		time.Sleep(edgeletAPISocketPoll)
	}
	return false
}

func isRetryableRuntimeDrainError(err error, stderr []byte) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error() + " " + string(stderr))
	return strings.Contains(msg, "no such file or directory") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "eof") ||
		strings.Contains(msg, "failed to read response") ||
		strings.Contains(msg, "runtime engine is not ready") ||
		strings.Contains(msg, "local_api_starting") ||
		strings.Contains(msg, "local api is starting")
}

type bootstrapCRIRuntime struct {
	client cri.WorkloadRuntime
}

func (r bootstrapCRIRuntime) ListLabeledRunning(ctx context.Context) ([]string, error) {
	return cri.ListLabeledRunningIDs(ctx, r.client)
}

func (r bootstrapCRIRuntime) StopContainer(ctx context.Context, id string, timeoutSec int64) error {
	return r.client.StopContainer(ctx, id, timeoutSec)
}

func newBootstrapCRIRuntime() (cri.WorkloadRuntime, func(), error) {
	if bootstrapTestNewCRIRuntime != nil {
		return bootstrapTestNewCRIRuntime()
	}
	client, err := cri.NewClient(constants.EdgeletContainerdSocket)
	if err != nil {
		return nil, nil, err
	}
	return client, func() { _ = client.Close() }, nil
}

// drainDataPlaneViaCRI stops labeled workloads through CRI while containerd is
// still up. It does not wait for the control-plane API socket.
func drainDataPlaneViaCRI(drainSec int) dataPlaneDrainOutcome {
	runtime, closer, err := newBootstrapCRIRuntime()
	if err != nil {
		logging.LogWarn("RUNTIME_BOOTSTRAP", "data-plane CRI drain unavailable")
		return dataPlaneDrainOutcome{timedOut: true}
	}
	if closer != nil {
		defer closer()
	}

	timeout := time.Duration(drainSec) * time.Second
	drainErr := processmanager.DrainLabeledWorkloads(context.Background(), bootstrapCRIRuntime{client: runtime}, timeout, processmanager.LabeledWorkloadStopTimeoutSec)
	if drainErr == nil {
		return dataPlaneDrainOutcome{complete: true}
	}
	if processmanager.IsLabeledWorkloadDrainTimeout(drainErr) {
		return dataPlaneDrainOutcome{timedOut: true}
	}
	logging.LogWarn("RUNTIME_BOOTSTRAP", fmt.Sprintf("data-plane CRI drain failed: %v", drainErr))
	return dataPlaneDrainOutcome{timedOut: true}
}

func dataPlaneVolumeDiskDirectory() string {
	cfg := config.GetInstance()
	if cfg != nil {
		if dir := strings.TrimSpace(cfg.DiskDirectory); dir != "" {
			return dir
		}
	}
	return constants.EdgeletDataDir
}

func outcomeFromQuiesce(result processmanager.DrainQuiesceResult) dataPlaneDrainOutcome {
	switch result.Status {
	case processmanager.DrainQuiesceComplete:
		return dataPlaneDrainOutcome{complete: true}
	case processmanager.DrainQuiesceVerifyFailed:
		return dataPlaneDrainOutcome{verifyFailed: true}
	default:
		return dataPlaneDrainOutcome{timedOut: true}
	}
}

// quiesceDataPlaneViaCRI is the data-plane stop drain: SIGTERM, then SIGKILL
// leftovers and volume holders, then verify before the caller may reap or stop.
func quiesceDataPlaneViaCRI(drainSec int) dataPlaneDrainOutcome {
	timeout := time.Duration(drainSec) * time.Second
	diskDir := dataPlaneVolumeDiskDirectory()
	deps := processmanager.QuiesceDeps{
		DiskDirectory:  diskDir,
		StopTimeoutSec: processmanager.LabeledWorkloadStopTimeoutSec,
	}
	if bootstrapTestVolumeHolders != nil {
		deps.VolumeHolders = bootstrapTestVolumeHolders
		deps.AfterKillWait = time.Millisecond
	}

	runtime, closer, err := newBootstrapCRIRuntime()
	if err != nil {
		logging.LogWarn("RUNTIME_BOOTSTRAP", "data-plane CRI drain unavailable")
		forceVolumeHoldersBestEffort(diskDir)
		if remaining, listErr := processmanager.ListVolumeTreeHolders(diskDir); listErr != nil || len(killableVolumeHolders(remaining)) > 0 {
			return dataPlaneDrainOutcome{verifyFailed: true}
		}
		return dataPlaneDrainOutcome{timedOut: true}
	}
	if closer != nil {
		defer closer()
	}

	wrapped := bootstrapCRIRuntime{client: runtime}
	deps.ListPIDs = func(ctx context.Context) ([]int, error) {
		return cri.ListLabeledPIDs(ctx, runtime)
	}
	deps.ListTasks = func(ctx context.Context) ([]string, error) {
		return cri.ListLabeledResidueIDs(ctx, runtime)
	}
	remainingStop := &atomic.Int64{}
	remainingStop.Store(processmanager.LabeledWorkloadStopTimeoutSec)
	deps.RemainingStop = remainingStop
	deps.Release = func(ctx context.Context) error {
		sec := remainingStop.Load()
		if sec < 1 {
			return errStopBudgetExhausted
		}
		return cri.ReleaseLabeledWorkloads(ctx, runtime, sec)
	}
	deps.ProtectPID = containerd.IsDataPlaneProtectedPID

	result := processmanager.QuiesceLabeledWorkloads(context.Background(), wrapped, timeout, deps)
	return outcomeFromQuiesce(result)
}

func forceVolumeHoldersBestEffort(diskDir string) {
	holders, err := processmanager.ListVolumeTreeHolders(diskDir)
	if err != nil {
		return
	}
	for _, pid := range killableVolumeHolders(holders) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

func killableVolumeHolders(pids []int) []int {
	if len(pids) == 0 {
		return nil
	}
	self := os.Getpid()
	out := make([]int, 0, len(pids))
	for _, pid := range pids {
		if pid <= 1 || pid == self || containerd.IsDataPlaneProtectedPID(pid) {
			continue
		}
		out = append(out, pid)
	}
	return out
}

func execRuntimeDrainCLI(drainSec int) dataPlaneDrainOutcome {
	bin, err := edgeletOperatorBinary()
	if err != nil {
		return dataPlaneDrainOutcome{degraded: true}
	}

	// Allow CLI/API overhead beyond the drain budget (total stop stays within 120s).
	outerBudget := time.Duration(drainSec+15) * time.Second
	deadline := time.Now().Add(edgeletAPISocketWaitBudget)

	for time.Now().Before(deadline) {
		if !waitForEdgeletAPISocket(edgeletAPISocketPoll) {
			time.Sleep(edgeletAPISocketPoll)
			continue
		}

		var stderr bytes.Buffer
		ctx, cancel := context.WithTimeout(context.Background(), outerBudget)
		cmd := exec.CommandContext(ctx, bin, "runtime", "drain", "--timeout", fmt.Sprintf("%d", drainSec)) // #nosec G204 -- operator binary from current edgelet argv/LookPath; fixed subcommand args
		cmd.Stdout = os.Stderr
		cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)

		runErr := cmd.Run()
		cancel()
		if runErr == nil {
			return dataPlaneDrainOutcome{complete: true}
		}

		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			switch exitErr.ExitCode() {
			case 1:
				stderrText := strings.ToLower(stderr.String())
				if strings.Contains(stderrText, "timed out") {
					return dataPlaneDrainOutcome{timedOut: true}
				}
				if isRetryableRuntimeDrainError(runErr, stderr.Bytes()) {
					time.Sleep(edgeletAPISocketPoll)
					continue
				}
				return dataPlaneDrainOutcome{degraded: true}
			default:
				if isRetryableRuntimeDrainError(runErr, stderr.Bytes()) {
					time.Sleep(edgeletAPISocketPoll)
					continue
				}
				return dataPlaneDrainOutcome{degraded: true}
			}
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return dataPlaneDrainOutcome{timedOut: true}
		}
		if isRetryableRuntimeDrainError(runErr, stderr.Bytes()) {
			time.Sleep(edgeletAPISocketPoll)
			continue
		}
		return dataPlaneDrainOutcome{degraded: true}
	}

	logging.LogWarn("RUNTIME_BOOTSTRAP", "edgelet API drain retries exhausted before data-plane stop")
	return dataPlaneDrainOutcome{degraded: true}
}

func edgeletOperatorBinary() (string, error) {
	if len(os.Args) > 0 {
		if path := os.Args[0]; path != "" {
			if resolved, err := exec.LookPath(path); err == nil {
				return resolved, nil
			}
			return path, nil
		}
	}
	return exec.LookPath("edgelet")
}

type runtimeBootstrapStopper interface {
	Stop()
}

type runtimeBootstrapStopDeps struct {
	shutdown                     runtimeBootstrapShutdownDeps
	reapManagedShimsUntilClear   func(socketPath string, remainingBudget time.Duration) error
	remainingStopBudget          func(stopStarted time.Time, totalBudget time.Duration) time.Duration
	setShimReapRemainingBudget   func(remaining time.Duration)
	resetShimReapRemainingBudget func()
	clearDrainVerifiedMarker     func() error
	writeDrainVerifiedMarker     func() error
	onVerifiedProceed            func()
	dataPlaneStopBudget          time.Duration
	postStopShimVerifyCap        time.Duration
	stopStarted                  func() time.Time
}

func defaultRuntimeBootstrapStopDeps() runtimeBootstrapStopDeps {
	return runtimeBootstrapStopDeps{
		shutdown:                     defaultRuntimeBootstrapShutdownDeps(),
		reapManagedShimsUntilClear:   containerd.ReapManagedShimsUntilClear,
		remainingStopBudget:          containerd.RemainingStopBudget,
		setShimReapRemainingBudget:   containerd.SetShimReapRemainingBudget,
		resetShimReapRemainingBudget: containerd.ResetShimReapRemainingBudget,
		clearDrainVerifiedMarker:     containerd.ClearDrainVerifiedMarker,
		writeDrainVerifiedMarker:     containerd.WriteDrainVerifiedMarker,
		dataPlaneStopBudget:          containerd.DefaultDataPlaneStopBudget,
		postStopShimVerifyCap:        containerd.DefaultPostStopShimVerifyCap,
		stopStarted:                  time.Now,
	}
}

func (d runtimeBootstrapStopDeps) withDefaults() runtimeBootstrapStopDeps {
	defaults := defaultRuntimeBootstrapStopDeps()
	if d.shutdown.shouldDrain == nil {
		d.shutdown.shouldDrain = defaults.shutdown.shouldDrain
	}
	if d.shutdown.drain == nil {
		d.shutdown.drain = defaults.shutdown.drain
	}
	if d.reapManagedShimsUntilClear == nil {
		d.reapManagedShimsUntilClear = defaults.reapManagedShimsUntilClear
	}
	if d.remainingStopBudget == nil {
		d.remainingStopBudget = defaults.remainingStopBudget
	}
	if d.setShimReapRemainingBudget == nil {
		d.setShimReapRemainingBudget = defaults.setShimReapRemainingBudget
	}
	if d.resetShimReapRemainingBudget == nil {
		d.resetShimReapRemainingBudget = defaults.resetShimReapRemainingBudget
	}
	if d.clearDrainVerifiedMarker == nil {
		d.clearDrainVerifiedMarker = defaults.clearDrainVerifiedMarker
	}
	if d.writeDrainVerifiedMarker == nil {
		d.writeDrainVerifiedMarker = defaults.writeDrainVerifiedMarker
	}
	if d.dataPlaneStopBudget <= 0 {
		d.dataPlaneStopBudget = defaults.dataPlaneStopBudget
	}
	if d.postStopShimVerifyCap <= 0 {
		d.postStopShimVerifyCap = defaults.postStopShimVerifyCap
	}
	if d.stopStarted == nil {
		d.stopStarted = defaults.stopStarted
	}
	return d
}

func stopEmbeddedContainerdDataPlane(socketPath string, drainSec int, svc runtimeBootstrapStopper, deps runtimeBootstrapStopDeps) dataPlaneDrainOutcome {
	deps = deps.withDefaults()
	stopStart := deps.stopStarted()
	totalBudget := deps.dataPlaneStopBudget

	if err := deps.clearDrainVerifiedMarker(); err != nil {
		logging.LogWarn("RUNTIME_BOOTSTRAP", fmt.Sprintf("failed to clear drain-verified marker: %v", err))
	}
	if err := processmanager.BeginDataPlaneDrainHold(); err != nil {
		logging.LogWarn("RUNTIME_BOOTSTRAP", fmt.Sprintf("failed to record data-plane drain hold: %v", err))
	}

	outcome := runDataPlaneDrainBeforeStop(drainSec, deps.shutdown)
	if !outcome.allowsShimReap() {
		if err := processmanager.EndDataPlaneDrainHold(); err != nil {
			logging.LogWarn("RUNTIME_BOOTSTRAP", fmt.Sprintf("failed to clear data-plane drain hold: %v", err))
		}
		logging.LogError(
			"RUNTIME_BOOTSTRAP",
			fmt.Sprintf("data-plane drain did not verify (status=%s); aborting containerd stop", outcome.status()),
			errDrainDidNotVerify,
		)
		return outcome
	}

	if err := deps.writeDrainVerifiedMarker(); err != nil {
		logging.LogWarn("RUNTIME_BOOTSTRAP", fmt.Sprintf("failed to write drain-verified marker: %v", err))
	}
	if deps.onVerifiedProceed != nil {
		deps.onVerifiedProceed()
	}

	// Stop the containerd child before shim reap. Reap while it is still
	// serving would signal the live child and shims attached to its socket.
	stopReapBudget := deps.remainingStopBudget(stopStart, totalBudget)
	deps.setShimReapRemainingBudget(stopReapBudget)
	defer deps.resetShimReapRemainingBudget()

	svc.Stop()

	primaryReapBudget := deps.remainingStopBudget(stopStart, totalBudget)
	var primaryReapErr error
	if primaryReapBudget <= 0 {
		logging.LogWarn("RUNTIME_BOOTSTRAP", "managed shim reap skipped: stop budget exhausted after containerd stop")
		primaryReapErr = errors.New("stop budget exhausted after containerd stop")
	} else if err := deps.reapManagedShimsUntilClear(socketPath, primaryReapBudget); err != nil {
		logging.LogWarn("RUNTIME_BOOTSTRAP", fmt.Sprintf("managed shim reap incomplete: %v", err))
		primaryReapErr = err
	}

	if primaryReapErr == nil {
		return outcome
	}

	verifyBudget := deps.remainingStopBudget(stopStart, totalBudget)
	if verifyBudget > deps.postStopShimVerifyCap {
		verifyBudget = deps.postStopShimVerifyCap
	}
	if verifyBudget <= 0 {
		return outcome
	}
	if verifyErr := deps.reapManagedShimsUntilClear(socketPath, verifyBudget); verifyErr != nil {
		logging.LogWarn("RUNTIME_BOOTSTRAP", fmt.Sprintf("post-stop managed shim verify incomplete: %v", verifyErr))
	}
	return outcome
}

var errDrainDidNotVerify = errors.New("data-plane drain did not verify")
