//go:build linux && cgo

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/constants"
	"github.com/eclipse-iofog/edgelet/internal/processmanager"
	"github.com/eclipse-iofog/edgelet/internal/workloadmeta"
	"github.com/eclipse-iofog/edgelet/pkg/containerd"
	"github.com/eclipse-iofog/edgelet/pkg/engine/edgelet/cri"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "edgelet-drain-hold")
	if err != nil {
		panic(err)
	}
	processmanager.SetDataPlaneDrainHoldPath(filepath.Join(dir, "drain-active"))
	m.Run()
	_ = os.RemoveAll(dir)
}

type fakeBootstrapStopService struct {
	stopCalled int
}

func (f *fakeBootstrapStopService) Stop() {
	f.stopCalled++
}

func TestRunDataPlaneDrainBeforeStop_InvokesDrainBeforeStop(t *testing.T) {
	t.Setenv("EDGELET_RUNTIME_SPLIT", "1")

	drainCalled := false
	stopSvc := &fakeBootstrapStopService{}
	deps := runtimeBootstrapShutdownDeps{
		shouldDrain: func() bool { return true },
		drain: func(drainSec int) dataPlaneDrainOutcome {
			if drainSec != 90 {
				t.Fatalf("expected drainSec=90, got %d", drainSec)
			}
			drainCalled = true
			return dataPlaneDrainOutcome{complete: true}
		},
	}

	runDataPlaneDrainBeforeStop(90, deps)
	stopSvc.Stop()

	if !drainCalled {
		t.Fatal("expected drain to be invoked on split runtime bootstrap stop")
	}
}

func TestRunDataPlaneDrainBeforeStop_MonolithicNoOp(t *testing.T) {
	t.Setenv("EDGELET_RUNTIME_SPLIT", "0")

	drainCalled := false
	deps := runtimeBootstrapShutdownDeps{
		shouldDrain: shouldDrainOnDataPlaneSIGTERM,
		drain: func(int) dataPlaneDrainOutcome {
			drainCalled = true
			return dataPlaneDrainOutcome{complete: true}
		},
	}

	runDataPlaneDrainBeforeStop(90, deps)
	if drainCalled {
		t.Fatal("expected drain to be skipped when runtime split is disabled")
	}
}

func TestRunDataPlaneDrainBeforeStop_DegradedProceed(t *testing.T) {
	deps := runtimeBootstrapShutdownDeps{
		shouldDrain: func() bool { return true },
		drain: func(int) dataPlaneDrainOutcome {
			return dataPlaneDrainOutcome{degraded: true}
		},
	}

	outcome := runDataPlaneDrainBeforeStop(45, deps)
	if outcome.complete || outcome.timedOut {
		t.Fatalf("expected degraded outcome, got %+v", outcome)
	}
}

func TestShouldDrainOnDataPlaneSIGTERM_SplitEnv(t *testing.T) {
	t.Setenv("EDGELET_RUNTIME_SPLIT", "1")
	if !shouldDrainOnDataPlaneSIGTERM() {
		t.Fatal("expected drain when EDGELET_RUNTIME_SPLIT=1")
	}
}

func TestShouldDrainOnDataPlaneSIGTERM_MonolithicEnv(t *testing.T) {
	t.Setenv("EDGELET_RUNTIME_SPLIT", "0")
	if shouldDrainOnDataPlaneSIGTERM() {
		t.Fatal("expected no drain when EDGELET_RUNTIME_SPLIT=0")
	}
}

type bootstrapCRIFake struct {
	mu            sync.Mutex
	running       map[string]*runtimeapi.Container
	stopCalls     int64
	activeStops   int64
	maxConcurrent int64
	stopDelay     time.Duration
	stopRemoves   bool
}

func newBootstrapCRIFake(ids ...string) *bootstrapCRIFake {
	running := make(map[string]*runtimeapi.Container, len(ids))
	for _, id := range ids {
		running[id] = &runtimeapi.Container{
			Id:    id,
			State: runtimeapi.ContainerState_CONTAINER_RUNNING,
			Labels: map[string]string{
				workloadmeta.LabelMicroserviceUID: "ms-" + id,
			},
		}
	}
	return &bootstrapCRIFake{running: running, stopRemoves: true}
}

func (f *bootstrapCRIFake) ListContainers(context.Context, *runtimeapi.ContainerFilter) ([]*runtimeapi.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*runtimeapi.Container, 0, len(f.running))
	for _, c := range f.running {
		out = append(out, c)
	}
	return out, nil
}

func (f *bootstrapCRIFake) StopContainer(_ context.Context, id string, _ int64) error {
	atomic.AddInt64(&f.stopCalls, 1)
	active := atomic.AddInt64(&f.activeStops, 1)
	defer atomic.AddInt64(&f.activeStops, -1)
	for {
		current := atomic.LoadInt64(&f.maxConcurrent)
		if active <= current {
			break
		}
		if atomic.CompareAndSwapInt64(&f.maxConcurrent, current, active) {
			break
		}
	}
	if f.stopDelay > 0 {
		time.Sleep(f.stopDelay)
	}
	if f.stopRemoves {
		f.mu.Lock()
		delete(f.running, id)
		f.mu.Unlock()
	}
	return nil
}

func installBootstrapCRIFake(t *testing.T, fake *bootstrapCRIFake) {
	t.Helper()
	bootstrapTestNewCRIRuntime = func() (cri.WorkloadRuntime, func(), error) {
		return fake, func() {}, nil
	}
	t.Cleanup(func() { bootstrapTestNewCRIRuntime = nil })
}

func TestDrainDataPlaneViaCRI_WithoutControlSocket(t *testing.T) {
	fake := newBootstrapCRIFake("c1")
	installBootstrapCRIFake(t, fake)

	waitCalled := false
	bootstrapTestWaitForAPISocket = func(time.Duration) bool {
		waitCalled = true
		return false
	}
	t.Cleanup(func() { bootstrapTestWaitForAPISocket = nil })

	outcome := drainDataPlaneViaCRI(2)
	if waitCalled {
		t.Fatal("data-plane CRI drain must not wait for the control-plane API socket")
	}
	if !outcome.complete || outcome.timedOut || outcome.degraded {
		t.Fatalf("expected complete CRI drain without control socket, got %+v", outcome)
	}
	if got := atomic.LoadInt64(&fake.stopCalls); got == 0 {
		t.Fatal("expected CRI StopContainer without a control-plane API socket")
	}
}

func TestDrainDataPlaneViaCRI_EmptyRunningSetFastComplete(t *testing.T) {
	fake := newBootstrapCRIFake()
	installBootstrapCRIFake(t, fake)

	start := time.Now()
	outcome := drainDataPlaneViaCRI(5)
	if !outcome.complete || outcome.timedOut || outcome.degraded {
		t.Fatalf("expected empty-set complete, got %+v", outcome)
	}
	if took := time.Since(start); took > 200*time.Millisecond {
		t.Fatalf("expected empty-set drain under 200ms, got %s", took)
	}
	if got := atomic.LoadInt64(&fake.stopCalls); got != 0 {
		t.Fatalf("expected no StopContainer on empty set, got %d", got)
	}
}

func TestDrainDataPlaneViaCRI_StopsConcurrently(t *testing.T) {
	ids := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		ids = append(ids, fmt.Sprintf("c%02d", i))
	}
	fake := newBootstrapCRIFake(ids...)
	fake.stopDelay = 80 * time.Millisecond
	installBootstrapCRIFake(t, fake)

	start := time.Now()
	outcome := drainDataPlaneViaCRI(2)
	elapsed := time.Since(start)
	if !outcome.complete || outcome.timedOut || outcome.degraded {
		t.Fatalf("expected concurrent CRI drain complete, got %+v", outcome)
	}
	serialFloor := time.Duration(len(ids)) * fake.stopDelay
	if elapsed >= serialFloor {
		t.Fatalf("expected concurrent StopContainer under %s serial floor, took %s", serialFloor, elapsed)
	}
	if peak := atomic.LoadInt64(&fake.maxConcurrent); peak <= 1 {
		t.Fatalf("expected concurrent StopContainer workers, maxConcurrent=%d", peak)
	}
	if got := atomic.LoadInt64(&fake.stopCalls); got < int64(len(ids)) {
		t.Fatalf("expected StopContainer for all %d containers, got %d", len(ids), got)
	}
}

func TestDefaultRuntimeBootstrapShutdownDeps_UsesCRIDrain(t *testing.T) {
	fake := newBootstrapCRIFake()
	installBootstrapCRIFake(t, fake)
	bootstrapTestVolumeHolders = func(string) ([]int, error) { return nil, nil }
	t.Cleanup(func() { bootstrapTestVolumeHolders = nil })

	deps := defaultRuntimeBootstrapShutdownDeps()
	if deps.drain == nil {
		t.Fatal("expected default data-plane drain")
	}
	outcome := deps.drain(1)
	if !outcome.complete {
		t.Fatalf("expected default drain to use CRI and complete, got %+v", outcome)
	}
}

func TestWaitForEdgeletAPISocket_NotReadyWithinBudget(t *testing.T) {
	// No production socket in unit-test environment; short budget should return false.
	if waitForEdgeletAPISocket(50 * time.Millisecond) {
		t.Fatal("expected waitForEdgeletAPISocket to time out without a listener")
	}
}

func TestIsRetryableRuntimeDrainError(t *testing.T) {
	cases := []struct {
		err    error
		stderr string
		want   bool
	}{
		{errors.New("dial unix /run/edgelet/edgelet.sock: connect: connection refused"), "", true},
		{errors.New("exit status 1"), "runtime engine is not ready", true},
		{errors.New("exit status 1"), "local_api_starting", true},
		{errors.New("exit status 1"), "local api is starting", true},
		{errors.New("exit status 1"), "failed to send Edgelet API request: Post \"http://unix/v1/runtime/drain\": EOF", true},
		{errors.New("exit status 1"), "drain timed out", false},
		{errors.New("exit status 1"), "permission denied", false},
	}
	for _, tc := range cases {
		if got := isRetryableRuntimeDrainError(tc.err, []byte(tc.stderr)); got != tc.want {
			t.Fatalf("isRetryableRuntimeDrainError(%q, %q) = %v, want %v", tc.err, tc.stderr, got, tc.want)
		}
	}
}

func TestExecRuntimeDrainCLI_RetriesBeforeSuccess(t *testing.T) {
	origArgs := os.Args
	t.Cleanup(func() { os.Args = origArgs })

	callFile := filepath.Join(t.TempDir(), "calls")
	scriptPath := filepath.Join(t.TempDir(), "edgelet")
	script := fmt.Sprintf(`#!/bin/sh
n=0
if [ -f %q ]; then
  read n < %q
fi
n=$((n+1))
echo "$n" > %q
if [ "$n" -lt 3 ]; then
  echo "connection refused" >&2
  exit 1
fi
exit 0
`, callFile, callFile, callFile)
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake edgelet script: %v", err)
	}

	os.Args = []string{scriptPath, "runtime-bootstrap"}
	bootstrapTestWaitForAPISocket = func(time.Duration) bool { return true }
	t.Cleanup(func() { bootstrapTestWaitForAPISocket = nil })

	outcome := execRuntimeDrainCLI(1)
	if !outcome.complete || outcome.degraded || outcome.timedOut {
		t.Fatalf("expected complete outcome after retries, got %+v", outcome)
	}
	data, err := os.ReadFile(callFile)
	if err != nil {
		t.Fatalf("read call count: %v", err)
	}
	if string(data) != "3\n" {
		t.Fatalf("expected 3 drain CLI attempts, got %q", string(data))
	}
}

func TestExecRuntimeDrainCLI_RetriesOnEOF(t *testing.T) {
	origArgs := os.Args
	t.Cleanup(func() { os.Args = origArgs })

	callFile := filepath.Join(t.TempDir(), "calls")
	scriptPath := filepath.Join(t.TempDir(), "edgelet")
	script := fmt.Sprintf(`#!/bin/sh
n=0
if [ -f %q ]; then
  read n < %q
fi
n=$((n+1))
echo "$n" > %q
if [ "$n" -eq 1 ]; then
  echo '✘ failed to send Edgelet API request: Post "http://unix/v1/runtime/drain": EOF' >&2
  exit 1
fi
exit 0
`, callFile, callFile, callFile)
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake edgelet script: %v", err)
	}

	os.Args = []string{scriptPath, "runtime-bootstrap"}
	bootstrapTestWaitForAPISocket = func(time.Duration) bool { return true }
	t.Cleanup(func() { bootstrapTestWaitForAPISocket = nil })

	outcome := execRuntimeDrainCLI(90)
	if !outcome.complete || outcome.degraded || outcome.timedOut {
		t.Fatalf("expected complete outcome after EOF retry, got %+v", outcome)
	}
}

func TestEdgeletOperatorBinary_UsesArgv0(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })
	os.Args = []string{"/usr/local/bin/edgelet", "runtime-bootstrap"}

	got, err := edgeletOperatorBinary()
	if err != nil {
		t.Fatalf("edgeletOperatorBinary: %v", err)
	}
	if got != "/usr/local/bin/edgelet" {
		t.Fatalf("expected argv0 binary, got %q", got)
	}
}

func TestStopEmbeddedContainerdDataPlane_DrainBeforeStopSinglePrimaryReap(t *testing.T) {
	var steps []string
	primaryReapCalls := 0
	svc := &fakeBootstrapStopService{}

	deps := defaultRuntimeBootstrapStopDeps()
	deps.shutdown.drain = func(drainSec int) dataPlaneDrainOutcome {
		steps = append(steps, fmt.Sprintf("drain:%d", drainSec))
		return dataPlaneDrainOutcome{complete: true}
	}
	deps.reapManagedShimsUntilClear = func(_ string, budget time.Duration) error {
		primaryReapCalls++
		if svc.stopCalled != 1 {
			t.Fatal("shim reap must run after containerd stop")
		}
		steps = append(steps, fmt.Sprintf("reap:%d", primaryReapCalls))
		if budget <= 0 {
			t.Fatalf("expected post-stop reap budget > 0, got %v", budget)
		}
		return nil
	}
	deps.remainingStopBudget = func(_ time.Time, totalBudget time.Duration) time.Duration {
		return totalBudget
	}
	deps.setShimReapRemainingBudget = func(_ time.Duration) {}
	deps.resetShimReapRemainingBudget = func() {}
	deps.clearDrainVerifiedMarker = func() error { return nil }
	deps.writeDrainVerifiedMarker = func() error { return nil }

	outcome := stopEmbeddedContainerdDataPlane(constants.EdgeletContainerdSocket, 90, svc, deps)
	if !outcome.complete || outcome.status() != "complete" {
		t.Fatalf("expected complete drain, got %+v status=%s", outcome, outcome.status())
	}

	if svc.stopCalled != 1 {
		t.Fatalf("expected svc.Stop once, got %d", svc.stopCalled)
	}
	if primaryReapCalls != 1 {
		t.Fatalf("expected single shim reap after stop, got %d", primaryReapCalls)
	}
	if len(steps) != 2 || steps[0] != "drain:90" || steps[1] != "reap:1" {
		t.Fatalf("unexpected stop pipeline order: %v", steps)
	}
}

func TestStopEmbeddedContainerdDataPlane_VerifyReapWhenPrimaryIncomplete(t *testing.T) {
	reapCalls := 0
	svc := &fakeBootstrapStopService{}

	deps := defaultRuntimeBootstrapStopDeps()
	deps.shutdown.drain = func(int) dataPlaneDrainOutcome {
		return dataPlaneDrainOutcome{complete: true}
	}
	deps.reapManagedShimsUntilClear = func(_ string, budget time.Duration) error {
		reapCalls++
		if svc.stopCalled != 1 {
			t.Fatal("shim reap must run after containerd stop")
		}
		if reapCalls == 1 {
			if budget <= 0 {
				t.Fatalf("expected post-stop reap budget > 0, got %v", budget)
			}
			return errors.New("managed runtime processes still running after reap attempts")
		}
		if budget <= 0 || budget > containerd.DefaultPostStopShimVerifyCap {
			t.Fatalf("expected verify reap budget in (0, %v], got %v", containerd.DefaultPostStopShimVerifyCap, budget)
		}
		return nil
	}
	deps.remainingStopBudget = func(_ time.Time, totalBudget time.Duration) time.Duration {
		return totalBudget
	}
	deps.setShimReapRemainingBudget = func(_ time.Duration) {}
	deps.resetShimReapRemainingBudget = func() {}
	deps.clearDrainVerifiedMarker = func() error { return nil }
	deps.writeDrainVerifiedMarker = func() error { return nil }

	stopEmbeddedContainerdDataPlane(constants.EdgeletContainerdSocket, 45, svc, deps)

	if svc.stopCalled != 1 {
		t.Fatalf("expected svc.Stop once, got %d", svc.stopCalled)
	}
	if reapCalls != 2 {
		t.Fatalf("expected primary + verify reap, got %d", reapCalls)
	}
}

func TestStopEmbeddedContainerdDataPlane_SIGTERMExitAllowsReap(t *testing.T) {
	reapCalls := 0
	proceedCalled := false
	markerWritten := false
	svc := &fakeBootstrapStopService{}

	deps := defaultRuntimeBootstrapStopDeps()
	deps.shutdown.drain = func(int) dataPlaneDrainOutcome {
		return dataPlaneDrainOutcome{complete: true}
	}
	deps.reapManagedShimsUntilClear = func(string, time.Duration) error {
		reapCalls++
		return nil
	}
	deps.remainingStopBudget = func(_ time.Time, total time.Duration) time.Duration { return total }
	deps.setShimReapRemainingBudget = func(time.Duration) {}
	deps.resetShimReapRemainingBudget = func() {}
	deps.clearDrainVerifiedMarker = func() error { return nil }
	deps.writeDrainVerifiedMarker = func() error {
		markerWritten = true
		return nil
	}
	deps.onVerifiedProceed = func() { proceedCalled = true }

	outcome := stopEmbeddedContainerdDataPlane(constants.EdgeletContainerdSocket, 30, svc, deps)
	if !outcome.allowsShimReap() || outcome.status() != "complete" {
		t.Fatalf("expected verified complete drain, got %+v", outcome)
	}
	if reapCalls == 0 {
		t.Fatal("expected shim reap after workloads exited on SIGTERM")
	}
	if !markerWritten {
		t.Fatal("expected drain-verified marker after verify pass")
	}
	if !proceedCalled {
		t.Fatal("expected verified-proceed hook after verify pass")
	}
	if svc.stopCalled != 1 {
		t.Fatalf("expected containerd stop after verify pass, got %d", svc.stopCalled)
	}
}

func TestStopEmbeddedContainerdDataPlane_IncompleteDrainDoesNotReap(t *testing.T) {
	reapCalls := 0
	proceedCalled := false
	svc := &fakeBootstrapStopService{}

	deps := defaultRuntimeBootstrapStopDeps()
	deps.shutdown.drain = func(int) dataPlaneDrainOutcome {
		return dataPlaneDrainOutcome{timedOut: true}
	}
	deps.reapManagedShimsUntilClear = func(string, time.Duration) error {
		reapCalls++
		return nil
	}
	deps.remainingStopBudget = func(_ time.Time, total time.Duration) time.Duration { return total }
	deps.setShimReapRemainingBudget = func(time.Duration) {}
	deps.resetShimReapRemainingBudget = func() {}
	deps.clearDrainVerifiedMarker = func() error { return nil }
	deps.writeDrainVerifiedMarker = func() error {
		t.Fatal("must not write drain-verified marker on incomplete drain")
		return nil
	}
	deps.onVerifiedProceed = func() { proceedCalled = true }

	outcome := stopEmbeddedContainerdDataPlane(constants.EdgeletContainerdSocket, 30, svc, deps)
	if outcome.allowsShimReap() || outcome.status() != "timedOut" {
		t.Fatalf("expected timed-out drain without reap, got %+v status=%s", outcome, outcome.status())
	}
	if reapCalls != 0 {
		t.Fatalf("incomplete drain must not reap shims, got %d reap calls", reapCalls)
	}
	if proceedCalled {
		t.Fatal("must not invoke binary-replace hook on incomplete drain")
	}
	if svc.stopCalled != 0 {
		t.Fatalf("must not stop containerd after incomplete drain, got %d", svc.stopCalled)
	}
}

func TestStopEmbeddedContainerdDataPlane_VerifyFailAbortsWithoutReap(t *testing.T) {
	reapCalls := 0
	proceedCalled := false
	svc := &fakeBootstrapStopService{}

	deps := defaultRuntimeBootstrapStopDeps()
	deps.shutdown.drain = func(int) dataPlaneDrainOutcome {
		return dataPlaneDrainOutcome{verifyFailed: true}
	}
	deps.reapManagedShimsUntilClear = func(string, time.Duration) error {
		reapCalls++
		return nil
	}
	deps.remainingStopBudget = func(_ time.Time, total time.Duration) time.Duration { return total }
	deps.setShimReapRemainingBudget = func(time.Duration) {}
	deps.resetShimReapRemainingBudget = func() {}
	deps.clearDrainVerifiedMarker = func() error { return nil }
	deps.writeDrainVerifiedMarker = func() error {
		t.Fatal("must not write drain-verified marker when verify fails")
		return nil
	}
	deps.onVerifiedProceed = func() { proceedCalled = true }

	outcome := stopEmbeddedContainerdDataPlane(constants.EdgeletContainerdSocket, 30, svc, deps)
	if outcome.allowsShimReap() || outcome.status() != "verifyFailed" {
		t.Fatalf("expected verifyFailed abort, got %+v status=%s", outcome, outcome.status())
	}
	if reapCalls != 0 {
		t.Fatalf("verify fail must not reap shims, got %d reap calls", reapCalls)
	}
	if proceedCalled {
		t.Fatal("must not invoke binary-replace hook when verify fails")
	}
	if svc.stopCalled != 0 {
		t.Fatalf("must not stop containerd when verify fails, got %d", svc.stopCalled)
	}
}

func TestStopEmbeddedContainerdDataPlane_DegradedDrainDoesNotReap(t *testing.T) {
	reapCalls := 0
	svc := &fakeBootstrapStopService{}

	deps := defaultRuntimeBootstrapStopDeps()
	deps.shutdown.drain = func(int) dataPlaneDrainOutcome {
		return dataPlaneDrainOutcome{degraded: true}
	}
	deps.reapManagedShimsUntilClear = func(string, time.Duration) error {
		reapCalls++
		return nil
	}
	deps.remainingStopBudget = func(_ time.Time, total time.Duration) time.Duration { return total }
	deps.setShimReapRemainingBudget = func(time.Duration) {}
	deps.resetShimReapRemainingBudget = func() {}
	deps.clearDrainVerifiedMarker = func() error { return nil }
	deps.writeDrainVerifiedMarker = func() error {
		t.Fatal("must not write drain-verified marker on degraded drain")
		return nil
	}

	outcome := stopEmbeddedContainerdDataPlane(constants.EdgeletContainerdSocket, 30, svc, deps)
	if outcome.allowsShimReap() || outcome.status() != "timedOut" {
		t.Fatalf("expected degraded drain to abort without reap, got %+v status=%s", outcome, outcome.status())
	}
	if reapCalls != 0 {
		t.Fatalf("degraded drain must not reap shims, got %d reap calls", reapCalls)
	}
	if svc.stopCalled != 0 {
		t.Fatalf("must not stop containerd after degraded drain, got %d", svc.stopCalled)
	}
}
