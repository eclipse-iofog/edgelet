//go:build linux && cgo

package containerd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestDescribeStartupFailureNilReturnsExitedEarly(t *testing.T) {
	err := describeStartupFailure(nil)
	if !errors.Is(err, ErrContainerdExitedEarly) {
		t.Fatalf("expected ErrContainerdExitedEarly, got: %v", err)
	}
	if strings.Contains(err.Error(), "%!w(<nil>)") {
		t.Fatalf("unexpected nil wrap artifact: %v", err)
	}
}

func TestStartPropagatesRunError(t *testing.T) {
	svc := NewService()
	svc.runFn = func() error {
		return errors.New("synthetic startup failure")
	}

	start := time.Now()
	err := svc.Start()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected startup error, got nil")
	}
	if !strings.Contains(err.Error(), "synthetic startup failure") {
		t.Fatalf("expected wrapped startup failure, got: %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Start should fail promptly, took %s", elapsed)
	}
}

func TestWaitReadyTimesOut(t *testing.T) {
	svc := NewService()
	svc.done = make(chan struct{})

	err := svc.WaitReady(25 * time.Millisecond)
	if err == nil {
		t.Fatal("expected readiness timeout, got nil")
	}
	if !errors.Is(err, ErrContainerdReadiness) {
		t.Fatalf("expected readiness error class, got: %v", err)
	}
}

func TestStopGracefulIsBoundedWhenDoneNeverCloses(t *testing.T) {
	svc := NewService()
	svc.done = make(chan struct{}) // keep open to emulate stuck shutdown
	svc.ctx, svc.cancel = context.WithCancel(context.Background())

	prev := containerdShutdownWaitTimeout
	containerdShutdownWaitTimeout = 25 * time.Millisecond
	defer func() { containerdShutdownWaitTimeout = prev }()

	start := time.Now()
	err := svc.StopGraceful()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected bounded timeout error, got nil")
	}
	if !errors.Is(err, ErrContainerdStopTimeout) {
		t.Fatalf("expected stop-timeout class, got: %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("StopGraceful should be bounded by timeout, took %s", elapsed)
	}
}

func TestStopForceIsBoundedWhenDoneNeverCloses(t *testing.T) {
	svc := NewService()
	svc.done = make(chan struct{}) // keep open to emulate stuck shutdown
	svc.ctx, svc.cancel = context.WithCancel(context.Background())

	prev := containerdShutdownWaitTimeout
	containerdShutdownWaitTimeout = 25 * time.Millisecond
	defer func() { containerdShutdownWaitTimeout = prev }()

	start := time.Now()
	err := svc.StopForce()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected bounded timeout error, got nil")
	}
	if !errors.Is(err, ErrContainerdStopTimeout) {
		t.Fatalf("expected stop-timeout class, got: %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("StopForce should be bounded by timeout, took %s", elapsed)
	}
}

func TestReapTimesOutWhenDoneNeverCloses(t *testing.T) {
	svc := NewService()
	svc.done = make(chan struct{}) // keep open to emulate stuck shutdown

	prev := containerdShutdownWaitTimeout
	containerdShutdownWaitTimeout = 25 * time.Millisecond
	defer func() { containerdShutdownWaitTimeout = prev }()

	start := time.Now()
	err := svc.Reap()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected reap timeout, got nil")
	}
	if !errors.Is(err, ErrContainerdStopTimeout) {
		t.Fatalf("expected stop-timeout class, got: %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Reap should be bounded by timeout, took %s", elapsed)
	}
}

func TestStopGracefulReturnsNilWhenServiceDone(t *testing.T) {
	svc := NewService()
	svc.ctx, svc.cancel = context.WithCancel(context.Background())
	svc.done = make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(10 * time.Millisecond)
		close(svc.done)
	}()

	prev := containerdShutdownWaitTimeout
	containerdShutdownWaitTimeout = 100 * time.Millisecond
	defer func() { containerdShutdownWaitTimeout = prev }()

	start := time.Now()
	err := svc.StopGraceful()
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected nil error when done closes, got: %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("StopGraceful should complete quickly, took %s", elapsed)
	}

	wg.Wait()
}

func TestReapManagedShimsEscalatesToSigkill(t *testing.T) {
	svc := NewService()

	prevBudgetFn := shimReapBudgetFn
	prevPoll := containerdShimReapPollInterval
	prevFinder := findManagedShimPIDs
	prevChildFinder := findContainerdChildPIDsForReap
	prevOrphanStop := stopOrphanedEmbeddedContainerdFn
	prevSignal := signalPID
	shimReapBudgetFn = func(_ time.Duration) (time.Duration, time.Duration) {
		return 30 * time.Millisecond, 30 * time.Millisecond
	}
	containerdShimReapPollInterval = 5 * time.Millisecond
	findContainerdChildPIDsForReap = func() ([]int, error) { return nil, nil }
	stopOrphanedEmbeddedContainerdFn = func() error { return nil }
	defer func() {
		shimReapBudgetFn = prevBudgetFn
		containerdShimReapPollInterval = prevPoll
		findManagedShimPIDs = prevFinder
		findContainerdChildPIDsForReap = prevChildFinder
		stopOrphanedEmbeddedContainerdFn = prevOrphanStop
		signalPID = prevSignal
	}()

	var mu sync.Mutex
	stage := "term"
	findManagedShimPIDs = func(_ string) ([]int, error) {
		mu.Lock()
		defer mu.Unlock()
		if stage == "term" {
			return []int{1234}, nil
		}
		return nil, nil
	}

	signals := make([]syscall.Signal, 0, 2)
	signalPID = func(_ int, sig syscall.Signal) error {
		mu.Lock()
		defer mu.Unlock()
		signals = append(signals, sig)
		if sig == syscall.SIGKILL {
			stage = "done"
		}
		return nil
	}

	if err := svc.reapManagedShims(); err != nil {
		t.Fatalf("expected reap to succeed, got: %v", err)
	}

	if len(signals) < 2 {
		t.Fatalf("expected SIGTERM then SIGKILL escalation, got %v", signals)
	}
	if signals[0] != syscall.SIGTERM {
		t.Fatalf("expected first signal SIGTERM, got %v", signals[0])
	}
	if signals[1] != syscall.SIGKILL {
		t.Fatalf("expected second signal SIGKILL, got %v", signals[1])
	}
}

func TestReapManagedShimsTimesOut(t *testing.T) {
	svc := NewService()

	prevBudgetFn := shimReapBudgetFn
	prevPoll := containerdShimReapPollInterval
	prevFinder := findManagedShimPIDs
	prevChildFinder := findContainerdChildPIDsForReap
	prevOrphanStop := stopOrphanedEmbeddedContainerdFn
	prevSignal := signalPID
	shimReapBudgetFn = func(_ time.Duration) (time.Duration, time.Duration) {
		return 20 * time.Millisecond, 20 * time.Millisecond
	}
	containerdShimReapPollInterval = 5 * time.Millisecond
	findContainerdChildPIDsForReap = func() ([]int, error) { return nil, nil }
	stopOrphanedEmbeddedContainerdFn = func() error { return nil }
	defer func() {
		shimReapBudgetFn = prevBudgetFn
		containerdShimReapPollInterval = prevPoll
		findManagedShimPIDs = prevFinder
		findContainerdChildPIDsForReap = prevChildFinder
		stopOrphanedEmbeddedContainerdFn = prevOrphanStop
		signalPID = prevSignal
	}()

	findManagedShimPIDs = func(_ string) ([]int, error) {
		return []int{2222}, nil
	}
	signalPID = func(_ int, _ syscall.Signal) error {
		return nil
	}

	err := svc.reapManagedShims()
	if err == nil {
		t.Fatal("expected timeout error while shim remains alive")
	}
	if !strings.Contains(err.Error(), "shims=[2222]") {
		t.Fatalf("expected still-running shim pids in error, got: %v", err)
	}
}

func TestShimReapBudgetRespectsRemainingCap(t *testing.T) {
	grace, force := shimReapBudget(45 * time.Second)
	if grace+force > DefaultShimReapBudgetCap+time.Millisecond {
		t.Fatalf("expected total reap budget capped at %s, got grace=%s force=%s", DefaultShimReapBudgetCap, grace, force)
	}

	grace, force = shimReapBudget(12 * time.Second)
	if grace+force > 12*time.Second+time.Millisecond {
		t.Fatalf("expected budget to follow remaining stop time, got grace=%s force=%s", grace, force)
	}
}

func TestRemainingStopBudgetNeverNegative(t *testing.T) {
	remaining := RemainingStopBudget(time.Now().Add(-5*time.Second), 2*time.Second)
	if remaining != 0 {
		t.Fatalf("expected zero remaining budget, got %s", remaining)
	}
}

func TestManagedShimCmdlineMatchCatalogHandlers(t *testing.T) {
	socket := "/run/edgelet/containerd.sock"
	cases := []struct {
		name    string
		cmdline string
		want    bool
	}{
		{
			name:    "spin",
			cmdline: "containerd-shim-spin-v2\x00-namespace\x00k8s.io\x00-address\x00" + socket,
			want:    true,
		},
		{
			name:    "edgelet-wasmtime",
			cmdline: "containerd-shim-edgelet-v2\x00-runtime\x00edgelet-wasmtime\x00address\x00" + socket,
			want:    true,
		},
		{
			name:    "other socket",
			cmdline: "containerd-shim-runc-v2\x00address\x00/run/other.sock",
			want:    false,
		},
		{
			name:    "not shim",
			cmdline: "crun\x00--root\x00/run/edgelet",
			want:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := managedShimCmdlineMatch([]byte(tc.cmdline), socket)
			if got != tc.want {
				t.Fatalf("managedShimCmdlineMatch(%q)=%v want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestReapManagedShimsSucceedsWhenAlreadyClear(t *testing.T) {
	prevFinder := findManagedShimPIDs
	prevChildFinder := findContainerdChildPIDsForReap
	prevOrphanStop := stopOrphanedEmbeddedContainerdFn
	findManagedShimPIDs = func(_ string) ([]int, error) { return nil, nil }
	findContainerdChildPIDsForReap = func() ([]int, error) { return nil, nil }
	stopOrphanedEmbeddedContainerdFn = func() error { return nil }
	defer func() {
		findManagedShimPIDs = prevFinder
		findContainerdChildPIDsForReap = prevChildFinder
		stopOrphanedEmbeddedContainerdFn = prevOrphanStop
	}()

	if err := ReapManagedShimsForSocket("/run/edgelet/containerd.sock", DefaultShimReapBudgetCap); err != nil {
		t.Fatalf("expected reap success with no shims, got: %v", err)
	}
}

func TestReapManagedShimsReportsContainerdChildren(t *testing.T) {
	prevFinder := findManagedShimPIDs
	prevChildFinder := findContainerdChildPIDsForReap
	prevOrphanStop := stopOrphanedEmbeddedContainerdFn
	findManagedShimPIDs = func(_ string) ([]int, error) { return nil, nil }
	findContainerdChildPIDsForReap = func() ([]int, error) { return []int{4321}, nil }
	stopOrphanedEmbeddedContainerdFn = func() error { return nil }
	defer func() {
		findManagedShimPIDs = prevFinder
		findContainerdChildPIDsForReap = prevChildFinder
		stopOrphanedEmbeddedContainerdFn = prevOrphanStop
	}()

	err := ReapManagedShimsForSocket("/run/edgelet/containerd.sock", DefaultShimReapBudgetCap)
	if err == nil {
		t.Fatal("expected incomplete reap when containerd child remains")
	}
	if !strings.Contains(err.Error(), "containerd_children=[4321]") {
		t.Fatalf("expected containerd child pids in error, got: %v", err)
	}
}

func TestReapManagedShimsUntilClear_RetriesUntilClear(t *testing.T) {
	prevFinder := findManagedShimPIDs
	prevChildFinder := findContainerdChildPIDsForReap
	prevOrphanStop := stopOrphanedEmbeddedContainerdFn
	prevSignal := signalPID
	attempts := 0
	findManagedShimPIDs = func(_ string) ([]int, error) {
		attempts++
		if attempts < 3 {
			return []int{4242}, nil
		}
		return nil, nil
	}
	findContainerdChildPIDsForReap = func() ([]int, error) { return nil, nil }
	stopOrphanedEmbeddedContainerdFn = func() error { return nil }
	signalPID = func(_ int, _ syscall.Signal) error { return nil }
	defer func() {
		findManagedShimPIDs = prevFinder
		findContainerdChildPIDsForReap = prevChildFinder
		stopOrphanedEmbeddedContainerdFn = prevOrphanStop
		signalPID = prevSignal
	}()

	if err := ReapManagedShimsUntilClear("/run/edgelet/containerd.sock", 5*time.Second); err != nil {
		t.Fatalf("expected until-clear reap to succeed after retries, got: %v", err)
	}
	if attempts < 3 {
		t.Fatalf("expected multiple discovery attempts, got %d", attempts)
	}
}

func TestReconfigureRestartsService(t *testing.T) {
	svc := NewService()
	svc.ctx, svc.cancel = context.WithCancel(context.Background())
	svc.done = make(chan struct{})
	close(svc.done) // emulate already stopped process for StopGraceful path

	prevWriteConfig := writeConfigForService
	prevFinder := findManagedShimPIDs
	prevReadLKG := readLKGForService
	prevWriteAtomic := writeAtomicForService
	prevSignalSelf := signalSelfForService
	prevRetryDelay := containerdReconfigureRetryDelay
	prevMaxAttempts := containerdReconfigureMaxAttempts
	prevStabilityWindow := containerdReconfigureStabilityWindow
	prevShutdownWait := containerdShutdownWaitTimeout
	t.Cleanup(func() {
		writeConfigForService = prevWriteConfig
		findManagedShimPIDs = prevFinder
		readLKGForService = prevReadLKG
		writeAtomicForService = prevWriteAtomic
		signalSelfForService = prevSignalSelf
		containerdReconfigureRetryDelay = prevRetryDelay
		containerdReconfigureMaxAttempts = prevMaxAttempts
		containerdReconfigureStabilityWindow = prevStabilityWindow
		containerdShutdownWaitTimeout = prevShutdownWait
	})
	writeConfigForService = func() error { return nil }
	findManagedShimPIDs = func(_ string) ([]int, error) { return nil, nil }
	readLKGForService = func(_ string) ([]byte, error) { return nil, errors.New("missing lkg") }
	writeAtomicForService = func(_ string, _ []byte, _ os.FileMode) error { return nil }
	signalSelfForService = func(_ syscall.Signal) error { return nil }
	containerdReconfigureRetryDelay = 1 * time.Millisecond
	containerdReconfigureMaxAttempts = 1
	containerdReconfigureStabilityWindow = 1 * time.Millisecond
	containerdShutdownWaitTimeout = 1 * time.Millisecond

	svc.runFn = func() error {
		svc.readyOnce.Do(func() { close(svc.ready) })
		<-svc.ctx.Done()
		close(svc.done)
		return nil
	}

	if err := svc.Reconfigure(); err != nil {
		t.Fatalf("expected reconfigure success, got: %v", err)
	}
}

func TestReconfigureReturnsDeterministicRestartError(t *testing.T) {
	svc := NewService()
	svc.ctx, svc.cancel = context.WithCancel(context.Background())
	svc.done = make(chan struct{})
	close(svc.done) // emulate already stopped process for StopGraceful path

	prevWriteConfig := writeConfigForService
	prevFinder := findManagedShimPIDs
	prevReadLKG := readLKGForService
	prevWriteAtomic := writeAtomicForService
	prevSignalSelf := signalSelfForService
	prevRetryDelay := containerdReconfigureRetryDelay
	prevMaxAttempts := containerdReconfigureMaxAttempts
	prevStabilityWindow := containerdReconfigureStabilityWindow
	prevShutdownWait := containerdShutdownWaitTimeout
	t.Cleanup(func() {
		writeConfigForService = prevWriteConfig
		findManagedShimPIDs = prevFinder
		readLKGForService = prevReadLKG
		writeAtomicForService = prevWriteAtomic
		signalSelfForService = prevSignalSelf
		containerdReconfigureRetryDelay = prevRetryDelay
		containerdReconfigureMaxAttempts = prevMaxAttempts
		containerdReconfigureStabilityWindow = prevStabilityWindow
		containerdShutdownWaitTimeout = prevShutdownWait
	})
	writeConfigForService = func() error { return nil }
	findManagedShimPIDs = func(_ string) ([]int, error) { return nil, nil }
	readLKGForService = func(_ string) ([]byte, error) { return nil, errors.New("missing lkg") }
	writeAtomicForService = func(_ string, _ []byte, _ os.FileMode) error { return nil }
	signaled := false
	signalSelfForService = func(_ syscall.Signal) error {
		signaled = true
		return nil
	}
	containerdReconfigureRetryDelay = 1 * time.Millisecond
	containerdReconfigureMaxAttempts = 1
	containerdReconfigureStabilityWindow = 1 * time.Millisecond
	containerdShutdownWaitTimeout = 1 * time.Millisecond

	svc.runFn = func() error {
		return errors.New("synthetic restart failure")
	}

	err := svc.Reconfigure()
	if err == nil {
		t.Fatal("expected reconfigure error")
	}
	if !errors.Is(err, ErrContainerdReconfigure) {
		t.Fatalf("expected ErrContainerdReconfigure, got: %v", err)
	}
	if !signaled {
		t.Fatal("expected daemon restart escalation signal on persistent failure")
	}
}

func TestReconfigureRollbackPathWritesLKGAndSkipsEscalationOnRecoveredRuntime(t *testing.T) {
	svc := NewService()
	svc.ctx, svc.cancel = context.WithCancel(context.Background())
	svc.done = make(chan struct{})
	close(svc.done)

	prevWriteConfig := writeConfigForService
	prevFinder := findManagedShimPIDs
	prevReadLKG := readLKGForService
	prevWriteAtomic := writeAtomicForService
	prevSignalSelf := signalSelfForService
	prevRetryDelay := containerdReconfigureRetryDelay
	prevMaxAttempts := containerdReconfigureMaxAttempts
	prevStabilityWindow := containerdReconfigureStabilityWindow
	prevShutdownWait := containerdShutdownWaitTimeout
	t.Cleanup(func() {
		writeConfigForService = prevWriteConfig
		findManagedShimPIDs = prevFinder
		readLKGForService = prevReadLKG
		writeAtomicForService = prevWriteAtomic
		signalSelfForService = prevSignalSelf
		containerdReconfigureRetryDelay = prevRetryDelay
		containerdReconfigureMaxAttempts = prevMaxAttempts
		containerdReconfigureStabilityWindow = prevStabilityWindow
		containerdShutdownWaitTimeout = prevShutdownWait
	})

	writeConfigForService = func() error { return nil }
	findManagedShimPIDs = func(_ string) ([]int, error) { return nil, nil }
	readLKGForService = func(_ string) ([]byte, error) { return []byte("version = 3\n"), nil }
	wroteRollback := false
	writeAtomicForService = func(path string, content []byte, _ os.FileMode) error {
		if strings.HasSuffix(path, "/config.toml") && bytes.Equal(content, []byte("version = 3\n")) {
			wroteRollback = true
		}
		return nil
	}
	signaled := false
	signalSelfForService = func(_ syscall.Signal) error {
		signaled = true
		return nil
	}
	containerdReconfigureRetryDelay = 1 * time.Millisecond
	containerdReconfigureMaxAttempts = 1
	containerdReconfigureStabilityWindow = 1 * time.Millisecond
	containerdShutdownWaitTimeout = 1 * time.Millisecond

	firstStart := true
	svc.runFn = func() error {
		if firstStart {
			firstStart = false
			close(svc.done)
			return errors.New("synthetic start failure")
		}
		svc.readyOnce.Do(func() { close(svc.ready) })
		<-svc.ctx.Done()
		close(svc.done)
		return nil
	}

	err := svc.Reconfigure()
	if err == nil {
		t.Fatal("expected reconfigure to report failure even after rollback recovery")
	}
	if !errors.Is(err, ErrContainerdReconfigure) {
		t.Fatalf("expected ErrContainerdReconfigure, got: %v", err)
	}
	if !wroteRollback {
		t.Fatal("expected rollback to write LKG config")
	}
	if signaled {
		t.Fatal("did not expect escalation signal when rollback restored runtime")
	}
}

func TestNotifyUnexpectedExitInvokesHandler(t *testing.T) {
	svc := NewService()
	called := make(chan error, 1)
	svc.SetUnexpectedExitHandler(func(err error) {
		called <- err
	})

	svc.notifyUnexpectedExit(errors.New("synthetic unexpected exit"))

	select {
	case err := <-called:
		if err == nil || !strings.Contains(err.Error(), "synthetic unexpected exit") {
			t.Fatalf("unexpected callback error payload: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected unexpected-exit handler callback")
	}
}

func TestSpawnChildSurfacesRuntimeResolutionError(t *testing.T) {
	prev := resolveChildExecutable
	resolveChildExecutable = func() (string, error) {
		return "", errors.New("missing fat runtime")
	}
	t.Cleanup(func() { resolveChildExecutable = prev })

	svc := NewService()
	_, err := svc.spawnChild()
	if err == nil || !strings.Contains(err.Error(), "resolve fat runtime") {
		t.Fatalf("expected resolution error, got %v", err)
	}
}

func withTempCleanupDirs(t *testing.T) (stateDir, runDir string) {
	stateDir = t.TempDir()
	runDir = t.TempDir()

	prevState := containerdStateDirForCleanup
	prevArtifactState := runtimeArtifactCleanupStateDir
	prevRun := runtimeArtifactCleanupRunDir
	prevSocket := runtimeArtifactCleanupSocket
	containerdStateDirForCleanup = stateDir
	runtimeArtifactCleanupStateDir = stateDir
	runtimeArtifactCleanupRunDir = runDir
	runtimeArtifactCleanupSocket = filepath.Join(runDir, "containerd.sock")
	t.Cleanup(func() {
		containerdStateDirForCleanup = prevState
		runtimeArtifactCleanupStateDir = prevArtifactState
		runtimeArtifactCleanupRunDir = prevRun
		runtimeArtifactCleanupSocket = prevSocket
	})
	return stateDir, runDir
}

func TestCleanupStaleRuntimeTasks_RemovesMissingAddress(t *testing.T) {
	stateDir, _ := withTempCleanupDirs(t)
	taskBase := filepath.Join(stateDir, runtimeV2TaskDirName)
	staleTask := filepath.Join(taskBase, "stale-task-id")
	if err := os.MkdirAll(staleTask, 0o755); err != nil {
		t.Fatalf("mkdir stale task: %v", err)
	}

	if err := CleanupStaleRuntimeTasks(); err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	if _, err := os.Stat(staleTask); !os.IsNotExist(err) {
		t.Fatalf("expected stale task dir removed, stat err=%v", err)
	}
}

func TestCleanupStaleRuntimeTasks_PreservesValidTask(t *testing.T) {
	stateDir, _ := withTempCleanupDirs(t)
	taskBase := filepath.Join(stateDir, runtimeV2TaskDirName)
	validTask := filepath.Join(taskBase, "valid-task-id")
	if err := os.MkdirAll(validTask, 0o755); err != nil {
		t.Fatalf("mkdir valid task: %v", err)
	}
	if err := os.WriteFile(filepath.Join(validTask, "address"), []byte("unix:///run/shim.sock"), 0o600); err != nil {
		t.Fatalf("write address file: %v", err)
	}

	if err := CleanupStaleRuntimeTasks(); err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	if _, err := os.Stat(validTask); err != nil {
		t.Fatalf("expected valid task dir preserved, stat err=%v", err)
	}
}

func TestCleanupStaleRuntimeTasks_PreservesRootDir(t *testing.T) {
	stateDir, _ := withTempCleanupDirs(t)
	rootDir := t.TempDir()
	rootMarker := filepath.Join(rootDir, "image-cache-marker")
	if err := os.WriteFile(rootMarker, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write root marker: %v", err)
	}

	taskBase := filepath.Join(stateDir, runtimeV2TaskDirName)
	staleTask := filepath.Join(taskBase, "orphan-task")
	if err := os.MkdirAll(staleTask, 0o755); err != nil {
		t.Fatalf("mkdir stale task: %v", err)
	}

	if err := CleanupStaleRuntimeTasks(); err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	if _, err := os.Stat(rootMarker); err != nil {
		t.Fatalf("expected root dir untouched, stat err=%v", err)
	}
}

func TestCleanupStaleRuntimeTasks_RemovesNestedNamespaceTasks(t *testing.T) {
	stateDir, _ := withTempCleanupDirs(t)
	taskBase := filepath.Join(stateDir, runtimeV2TaskDirName, "k8s.io")
	staleTask := filepath.Join(taskBase, "stale-task-id")
	if err := os.MkdirAll(staleTask, 0o755); err != nil {
		t.Fatalf("mkdir stale task: %v", err)
	}
	validTask := filepath.Join(taskBase, "valid-task-id")
	if err := os.MkdirAll(validTask, 0o755); err != nil {
		t.Fatalf("mkdir valid task: %v", err)
	}
	if err := os.WriteFile(filepath.Join(validTask, "address"), []byte("unix:///run/shim.sock"), 0o600); err != nil {
		t.Fatalf("write address file: %v", err)
	}

	if err := CleanupStaleRuntimeTasks(); err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	if _, err := os.Stat(staleTask); !os.IsNotExist(err) {
		t.Fatalf("expected nested stale task dir removed, stat err=%v", err)
	}
	if _, err := os.Stat(validTask); err != nil {
		t.Fatalf("expected nested valid task dir preserved, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, runtimeV2TaskDirName, "k8s.io")); err != nil {
		t.Fatalf("expected namespace dir preserved, stat err=%v", err)
	}
}

func TestCleanupRuntimeArtifacts_StillOnRetry(t *testing.T) {
	stateDir, runDir := withTempCleanupDirs(t)
	stateMarker := filepath.Join(stateDir, "stale-state-marker")
	if err := os.WriteFile(stateMarker, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write state marker: %v", err)
	}
	controlSocket := filepath.Join(runDir, "edgelet.sock")
	if err := os.WriteFile(controlSocket, []byte("sock"), 0o600); err != nil {
		t.Fatalf("write control socket: %v", err)
	}

	if err := CleanupRuntimeArtifacts(); err != nil {
		t.Fatalf("artifact cleanup failed: %v", err)
	}
	if _, err := os.Stat(stateMarker); !os.IsNotExist(err) {
		t.Fatalf("expected state marker removed, stat err=%v", err)
	}
	if _, err := os.Stat(stateDir); err != nil {
		t.Fatalf("expected state dir recreated, stat err=%v", err)
	}
	if _, err := os.Stat(runDir); err != nil {
		t.Fatalf("expected run dir recreated, stat err=%v", err)
	}
	if _, err := os.Stat(controlSocket); err != nil {
		t.Fatalf("expected control-plane socket preserved, stat err=%v", err)
	}
	if _, err := os.Stat(runtimeArtifactCleanupSocket); !os.IsNotExist(err) {
		t.Fatalf("expected containerd socket removed, stat err=%v", err)
	}
}

func TestCleanupStaleRuntimeTasks_PreservesNamespaceDirectory(t *testing.T) {
	stateDir, _ := withTempCleanupDirs(t)
	namespaceDir := filepath.Join(stateDir, runtimeV2TaskDirName, "k8s.io")
	unmarked := filepath.Join(namespaceDir, "wasm-task")
	if err := os.MkdirAll(unmarked, 0o755); err != nil {
		t.Fatalf("mkdir unmarked task: %v", err)
	}
	if err := os.WriteFile(filepath.Join(unmarked, "log.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	if err := CleanupStaleRuntimeTasks(); err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	if _, err := os.Stat(namespaceDir); err != nil {
		t.Fatalf("expected namespace directory preserved, stat err=%v", err)
	}
	if _, err := os.Stat(unmarked); !os.IsNotExist(err) {
		t.Fatalf("expected unmarked task directory removed, stat err=%v", err)
	}
}

func TestCleanupStaleRuntimeTasks_ContinuesOnEBUSY(t *testing.T) {
	stateDir, _ := withTempCleanupDirs(t)
	taskBase := filepath.Join(stateDir, runtimeV2TaskDirName)
	busyTask := filepath.Join(taskBase, "busy-task-id")
	if err := os.MkdirAll(busyTask, 0o755); err != nil {
		t.Fatalf("mkdir busy task: %v", err)
	}

	prevRemove := removeStaleRuntimeTaskDir
	removeStaleRuntimeTaskDir = func(path string) error {
		if path == busyTask {
			return syscall.EBUSY
		}
		return os.RemoveAll(path)
	}
	defer func() { removeStaleRuntimeTaskDir = prevRemove }()

	if err := CleanupStaleRuntimeTasksWithRetry(); err != nil {
		t.Fatalf("expected bootstrap to continue on EBUSY, got: %v", err)
	}
	if _, err := os.Stat(busyTask); err != nil {
		t.Fatalf("expected busy task dir to remain, stat err=%v", err)
	}
}

func TestCleanupStaleRuntimeTasks_RetriesAfterShimReap(t *testing.T) {
	stateDir, _ := withTempCleanupDirs(t)
	taskBase := filepath.Join(stateDir, runtimeV2TaskDirName)
	busyTask := filepath.Join(taskBase, "busy-task-id")
	if err := os.MkdirAll(busyTask, 0o755); err != nil {
		t.Fatalf("mkdir busy task: %v", err)
	}

	attempts := 0
	prevRemove := removeStaleRuntimeTaskDir
	removeStaleRuntimeTaskDir = func(path string) error {
		if path != busyTask {
			return os.RemoveAll(path)
		}
		attempts++
		if attempts == 1 {
			return syscall.EBUSY
		}
		return os.RemoveAll(path)
	}
	defer func() { removeStaleRuntimeTaskDir = prevRemove }()

	prevSleep := staleTaskCleanupSleep
	staleTaskCleanupSleep = func(time.Duration) {}
	defer func() { staleTaskCleanupSleep = prevSleep }()

	if err := CleanupStaleRuntimeTasksWithRetry(); err != nil {
		t.Fatalf("expected cleanup to succeed after retry, got: %v", err)
	}
	if _, err := os.Stat(busyTask); !os.IsNotExist(err) {
		t.Fatalf("expected busy task dir removed after retry, stat err=%v", err)
	}
	if attempts < 2 {
		t.Fatalf("expected at least two remove attempts, got %d", attempts)
	}
}

func TestCleanupStaleRuntimeTasks_ReturnsFatalOnNonEBUSY(t *testing.T) {
	stateDir, _ := withTempCleanupDirs(t)
	taskBase := filepath.Join(stateDir, runtimeV2TaskDirName)
	staleTask := filepath.Join(taskBase, "stale-task-id")
	if err := os.MkdirAll(staleTask, 0o755); err != nil {
		t.Fatalf("mkdir stale task: %v", err)
	}

	prevRemove := removeStaleRuntimeTaskDir
	removeStaleRuntimeTaskDir = func(path string) error {
		return fmt.Errorf("permission denied removing %s", path)
	}
	defer func() { removeStaleRuntimeTaskDir = prevRemove }()

	err := CleanupStaleRuntimeTasksWithRetry()
	if err == nil {
		t.Fatal("expected fatal cleanup error")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("expected permission error, got: %v", err)
	}
}

func TestCleanupRuntimeArtifacts_SkippedWhenShimsRemain(t *testing.T) {
	prevFindShims := findManagedShimPIDs
	findManagedShimPIDs = func(_ string) ([]int, error) {
		return []int{4242}, nil
	}
	defer func() { findManagedShimPIDs = prevFindShims }()

	prevOrphanStop := stopOrphanedEmbeddedContainerdFn
	stopOrphanedEmbeddedContainerdFn = func() error { return nil }
	defer func() { stopOrphanedEmbeddedContainerdFn = prevOrphanStop }()

	stateDir, _ := withTempCleanupDirs(t)
	stateMarker := filepath.Join(stateDir, "stale-state-marker")
	if err := os.WriteFile(stateMarker, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write state marker: %v", err)
	}

	err := reapManagedShimsForSocket(runtimeArtifactCleanupSocket, DefaultShimReapBudgetCap)
	if err == nil {
		t.Fatal("expected shim reap to fail while shims remain")
	}
	if !strings.Contains(err.Error(), "managed runtime processes still running") {
		t.Fatalf("expected incomplete reap error, got: %v", err)
	}
	if _, statErr := os.Stat(stateMarker); statErr != nil {
		t.Fatalf("expected state marker preserved while shims remain, stat err=%v", statErr)
	}
}

func TestCRIRuntimeUsable_NoSocket(t *testing.T) {
	if criRuntimeUsable() {
		t.Fatal("expected CRI probe to fail without containerd socket")
	}
}
