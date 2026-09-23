//go:build linux

package containerd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
)

const (
	// DefaultDataPlaneStopBudget is the aligned systemd TimeoutStopSec budget for data-plane stop.
	DefaultDataPlaneStopBudget = 120 * time.Second
	// DefaultShimReapBudgetCap is the maximum time spent reaping managed shims during stop/bootstrap.
	DefaultShimReapBudgetCap = 30 * time.Second
	// DefaultPostStopShimVerifyCap is the maximum verify reap after containerd stop when primary reap failed.
	DefaultPostStopShimVerifyCap = 10 * time.Second
)

const (
	deleteShimSIGTERMGrace   = 500 * time.Millisecond
	shimReapUntilClearMinGap = 200 * time.Millisecond
)

var (
	containerdShimReapPollInterval   = 100 * time.Millisecond
	shimReapBudgetFn                 = shimReapBudget
	findManagedShimPIDs              = findManagedShimPIDsFromProc
	findManagedShimPIDsForTaskFn     = findManagedShimPIDsForTaskFromProc
	findContainerdChildPIDsForReap   = findContainerdChildPIDs
	stopOrphanedEmbeddedContainerdFn = stopOrphanedEmbeddedContainerdFromProc
	signalPID                        = syscall.Kill
)

var shimReapLogger = logging.NewModuleLogger("Containerd")

func shimReapBudget(remaining time.Duration) (grace, force time.Duration) {
	budget := remaining
	if budget > DefaultShimReapBudgetCap {
		budget = DefaultShimReapBudgetCap
	}
	if budget <= 0 {
		return 0, 0
	}
	grace = budget * 3 / 5
	force = budget - grace
	const minSlice = 100 * time.Millisecond
	if grace < minSlice {
		grace = minSlice
	}
	if force < minSlice {
		force = minSlice
	}
	return grace, force
}

// ReapManagedShimsForSocket terminates containerd-shim processes bound to socketPath
// and stops orphaned --edgelet-containerd-child processes for the edgelet socket scope.
func ReapManagedShimsForSocket(socketPath string, remainingBudget time.Duration) error {
	return reapManagedShimsForSocket(socketPath, remainingBudget)
}

// ReapManagedShimsUntilClear retries managed shim/child teardown until processes are gone
// or remainingBudget is exhausted.
func ReapManagedShimsUntilClear(socketPath string, remainingBudget time.Duration) error {
	if remainingBudget <= 0 {
		return verifyRuntimeProcessesCleared(socketPath)
	}

	deadline := time.Now().Add(remainingBudget)
	var lastErr error
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if lastErr != nil {
				return lastErr
			}
			return verifyRuntimeProcessesCleared(socketPath)
		}

		lastErr = reapManagedShimsForSocket(socketPath, remaining)
		if lastErr == nil {
			return nil
		}
		if !isRetryableReapIncomplete(lastErr) {
			return lastErr
		}

		sleep := shimReapUntilClearMinGap
		if remaining < sleep {
			sleep = remaining
		}
		if sleep > 0 {
			time.Sleep(sleep)
		}
	}
}

func isRetryableReapIncomplete(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "managed runtime processes still running") ||
		strings.Contains(msg, "wait for shim sigterm completion") ||
		strings.Contains(msg, "wait for shim sigkill completion")
}

func reapManagedShimsForSocket(socketPath string, remainingBudget time.Duration) error {
	if pid := currentLiveContainerdChildPID(); pid > 0 {
		return fmt.Errorf("containerd child %d is still running; shim reap runs after it stops", pid)
	}
	graceTimeout, forceTimeout := shimReapBudgetFn(remainingBudget)

	remaining, err := findManagedShimPIDs(socketPath)
	if err != nil {
		return fmt.Errorf("discover managed shims before reap: %w", err)
	}
	if len(remaining) > 0 {
		shimReapLogger.Warnf("Detected %d managed containerd shim process(es) after containerd stop; reaping", len(remaining))
		if err := signalManagedShims(remaining, syscall.SIGTERM); err != nil {
			return err
		}

		deleteGrace, normalGrace := graceTimeout, graceTimeout
		if deleteGrace > deleteShimSIGTERMGrace {
			deleteGrace = deleteShimSIGTERMGrace
		}
		if err := waitForManagedShimSubsetExit(socketPath, deleteGrace, isDeleteStateShimCmdline); err != nil {
			return fmt.Errorf("wait for delete shim SIGTERM completion: %w", err)
		}
		if err := waitForManagedShimSubsetExit(socketPath, normalGrace, isNotDeleteStateShimCmdline); err != nil {
			return fmt.Errorf("wait for shim SIGTERM completion: %w", err)
		}

		remaining, err = findManagedShimPIDs(socketPath)
		if err != nil {
			return fmt.Errorf("discover managed shims before SIGKILL: %w", err)
		}
		if len(remaining) > 0 {
			if err := signalManagedShims(remaining, syscall.SIGKILL); err != nil {
				return err
			}
			if exited, waitErr := waitForManagedShimsExit(socketPath, forceTimeout); waitErr != nil {
				return fmt.Errorf("wait for shim SIGKILL completion: %w", waitErr)
			} else if !exited {
				return reportShimReapIncomplete(socketPath)
			}
		}
	}

	if err := stopOrphanedEmbeddedContainerdFn(); err != nil {
		return fmt.Errorf("stop orphaned embedded containerd children: %w", err)
	}
	return verifyRuntimeProcessesCleared(socketPath)
}

// ReapShimsForStaleTask terminates managed shims bound to a stale runtime task ID.
func ReapShimsForStaleTask(socketPath, taskID string, remainingBudget time.Duration) error {
	if strings.TrimSpace(taskID) == "" {
		return nil
	}
	pids, err := findManagedShimPIDsForTaskFn(socketPath, taskID)
	if err != nil {
		return err
	}
	if len(pids) == 0 {
		return nil
	}
	shimReapLogger.Warnf("Reaping %d managed shim process(es) for stale task %s", len(pids), taskID)
	if err := signalManagedShims(pids, syscall.SIGTERM); err != nil {
		return err
	}
	grace := deleteShimSIGTERMGrace
	if remainingBudget > 0 && remainingBudget < grace {
		grace = remainingBudget
	}
	if _, waitErr := waitForManagedShimPIDsExit(pids, grace); waitErr != nil {
		return waitErr
	}
	pids, err = findManagedShimPIDsForTaskFn(socketPath, taskID)
	if err != nil {
		return err
	}
	if len(pids) == 0 {
		return nil
	}
	if err := signalManagedShims(pids, syscall.SIGKILL); err != nil {
		return err
	}
	force := remainingBudget - grace
	if force <= 0 {
		force = deleteShimSIGTERMGrace
	}
	exited, waitErr := waitForManagedShimPIDsExit(pids, force)
	if waitErr != nil {
		return waitErr
	}
	if !exited {
		return fmt.Errorf("managed shims still running for stale task %s: %v", taskID, pids)
	}
	return nil
}

func signalManagedShims(pids []int, sig syscall.Signal) error {
	for _, pid := range pids {
		if err := signalPID(pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
			shimReapLogger.Warnf("Failed to signal shim pid %d with %v: %v", pid, sig, err)
		}
	}
	return nil
}

func verifyRuntimeProcessesCleared(socketPath string) error {
	shims, shimErr := findManagedShimPIDs(socketPath)
	children, childErr := findContainerdChildPIDsForReap()
	if shimErr != nil {
		return fmt.Errorf("verify managed shims cleared: %w", shimErr)
	}
	if childErr != nil {
		return fmt.Errorf("verify containerd children cleared: %w", childErr)
	}
	if len(shims) > 0 || len(children) > 0 {
		return reportShimReapIncompleteWithPIDs(shims, children)
	}
	shimReapLogger.Infof("shim_reap_complete")
	return nil
}

func reportShimReapIncomplete(socketPath string) error {
	shims, _ := findManagedShimPIDs(socketPath)
	children, _ := findContainerdChildPIDsForReap()
	return reportShimReapIncompleteWithPIDs(shims, children)
}

func reportShimReapIncompleteWithPIDs(shims, children []int) error {
	shimReapLogger.Warnf("shim_reap_incomplete remaining_shims=%v remaining_containerd_children=%v", shims, children)
	return fmt.Errorf("managed runtime processes still running after reap attempts: shims=%v containerd_children=%v", shims, children)
}

func managedShimCmdlineMatch(cmdline []byte, socketPath string) bool {
	return bytes.Contains(cmdline, []byte("containerd-shim-")) && bytes.Contains(cmdline, []byte(socketPath))
}

func isDeleteStateShimCmdline(cmdline []byte) bool {
	return bytes.Contains(cmdline, []byte(" delete"))
}

func isNotDeleteStateShimCmdline(cmdline []byte) bool {
	return !isDeleteStateShimCmdline(cmdline)
}

func managedShimTaskMatch(cmdline []byte, socketPath, taskID string) bool {
	if !managedShimCmdlineMatch(cmdline, socketPath) {
		return false
	}
	idToken := []byte("-id " + taskID)
	bundleToken := []byte(taskID)
	return bytes.Contains(cmdline, idToken) || bytes.Contains(cmdline, bundleToken)
}

func findManagedShimPIDsForTaskFromProc(socketPath, taskID string) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("read /proc: %w", err)
	}

	pids := make([]int, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil || pid <= 0 {
			continue
		}
		cmdline, readErr := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if readErr != nil {
			continue
		}
		if !managedShimTaskMatch(cmdline, socketPath, taskID) {
			continue
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

func waitForManagedShimSubsetExit(socketPath string, timeout time.Duration, match func([]byte) bool) error {
	deadline := time.Now().Add(timeout)
	for {
		pending, err := findManagedShimPIDsMatching(socketPath, match)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return nil
		}
		time.Sleep(containerdShimReapPollInterval)
	}
}

func waitForManagedShimPIDsExit(pids []int, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		alive := make([]int, 0, len(pids))
		for _, pid := range pids {
			if err := signalPID(pid, 0); err == nil {
				alive = append(alive, pid)
			} else if !errors.Is(err, syscall.ESRCH) {
				return false, err
			}
		}
		if len(alive) == 0 {
			return true, nil
		}
		pids = alive
		if time.Now().After(deadline) {
			return false, nil
		}
		time.Sleep(containerdShimReapPollInterval)
	}
}

func findManagedShimPIDsMatching(socketPath string, match func([]byte) bool) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("read /proc: %w", err)
	}

	pids := make([]int, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil || pid <= 0 {
			continue
		}
		cmdline, readErr := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if readErr != nil {
			continue
		}
		if !managedShimCmdlineMatch(cmdline, socketPath) || !match(cmdline) {
			continue
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

func waitForManagedShimsExit(socketPath string, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		pids, err := findManagedShimPIDs(socketPath)
		if err != nil {
			return false, err
		}
		if len(pids) == 0 {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		time.Sleep(containerdShimReapPollInterval)
	}
}

func findManagedShimPIDsFromProc(socketPath string) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("read /proc: %w", err)
	}

	pids := make([]int, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil || pid <= 0 {
			continue
		}
		cmdline, readErr := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if readErr != nil {
			continue
		}
		if !managedShimCmdlineMatch(cmdline, socketPath) {
			continue
		}
		pids = append(pids, pid)
	}
	return pids, nil
}
