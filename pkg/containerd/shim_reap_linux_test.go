//go:build linux

package containerd

import (
	"syscall"
	"testing"
	"time"
)

func TestIsDeleteStateShimCmdline(t *testing.T) {
	cmdline := []byte("containerd-shim-spin-v2 -namespace k8s.io -address /run/edgelet/containerd.sock delete")
	if !isDeleteStateShimCmdline(cmdline) {
		t.Fatal("expected delete-state shim cmdline to match")
	}
	if isDeleteStateShimCmdline([]byte("containerd-shim-runc-v2 -address /run/edgelet/containerd.sock")) {
		t.Fatal("expected non-delete shim cmdline to not match")
	}
}

func TestManagedShimTaskMatch(t *testing.T) {
	taskID := "abc123"
	cmdline := []byte("containerd-shim-spin-v2 -address /run/edgelet/containerd.sock -id abc123 -bundle /state/abc123 delete")
	if !managedShimTaskMatch(cmdline, "/run/edgelet/containerd.sock", taskID) {
		t.Fatal("expected task-specific shim match")
	}
	if managedShimTaskMatch(cmdline, "/run/edgelet/containerd.sock", "other") {
		t.Fatal("expected different task ID to not match")
	}
}

func TestReapShimsForStaleTask_NoShims(t *testing.T) {
	prevFinder := findManagedShimPIDsForTaskFn
	findManagedShimPIDsForTaskFn = func(_, _ string) ([]int, error) {
		return nil, nil
	}
	defer func() { findManagedShimPIDsForTaskFn = prevFinder }()

	if err := ReapShimsForStaleTask("/run/edgelet/containerd.sock", "task-id", time.Second); err != nil {
		t.Fatalf("expected no-op when no shims, got: %v", err)
	}
}

func TestReapManagedShimsForSocket_RefusesWhileContainerdChildIsLive(t *testing.T) {
	setLiveContainerdChildPID(4242)
	t.Cleanup(func() { clearLiveContainerdChildPID(4242) })
	err := reapManagedShimsForSocket("/run/edgelet/containerd.sock", time.Second)
	if err == nil {
		t.Fatal("expected shim reap to wait until the containerd child has stopped")
	}
}

func TestReapManagedShimsForSocket_FastDeleteShimPath(t *testing.T) {
	prevFinder := findManagedShimPIDs
	prevSignal := signalPID
	prevPoll := containerdShimReapPollInterval
	prevStopOrphan := stopOrphanedEmbeddedContainerdFn
	prevChildFinder := findContainerdChildPIDsForReap

	attempts := 0
	findManagedShimPIDs = func(_ string) ([]int, error) {
		attempts++
		if attempts == 1 {
			return []int{4242}, nil
		}
		return nil, nil
	}
	signalPID = func(pid int, sig syscall.Signal) error {
		if pid == 4242 && (sig == syscall.SIGTERM || sig == syscall.SIGKILL) {
			return nil
		}
		return syscall.Kill(pid, sig)
	}
	stopOrphanedEmbeddedContainerdFn = func() error { return nil }
	findContainerdChildPIDsForReap = func() ([]int, error) { return nil, nil }
	containerdShimReapPollInterval = 1 * time.Millisecond
	defer func() {
		findManagedShimPIDs = prevFinder
		signalPID = prevSignal
		containerdShimReapPollInterval = prevPoll
		stopOrphanedEmbeddedContainerdFn = prevStopOrphan
		findContainerdChildPIDsForReap = prevChildFinder
	}()

	if err := reapManagedShimsForSocket("/run/edgelet/containerd.sock", 2*time.Second); err != nil {
		t.Fatalf("expected delete shim fast reap to succeed, got: %v", err)
	}
}
