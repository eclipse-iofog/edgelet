//go:build linux

package containerd

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/constants"
)

const orphanStopGrace = 5 * time.Second

// liveContainerdChildPID is the child started by the data-plane service.
// Orphan reap and volume force-kill must not signal it.
var liveContainerdChildPID atomic.Int64

func setLiveContainerdChildPID(pid int) {
	if pid <= 0 {
		return
	}
	liveContainerdChildPID.Store(int64(pid))
}

func clearLiveContainerdChildPID(pid int) {
	if pid <= 0 {
		return
	}
	liveContainerdChildPID.CompareAndSwap(int64(pid), 0)
}

func currentLiveContainerdChildPID() int {
	return int(liveContainerdChildPID.Load())
}

// IsDataPlaneProtectedPID reports processes force-kill must leave alone: the
// data-plane parent, the live containerd child, and shims still attached to
// the edgelet containerd socket.
func IsDataPlaneProtectedPID(pid int) bool {
	if pid <= 1 || pid == os.Getpid() {
		return true
	}
	if pid == currentLiveContainerdChildPID() {
		return true
	}
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	if managedShimCmdlineMatch(cmdline, constants.EdgeletContainerdSocket) {
		return true
	}
	text := strings.ReplaceAll(string(cmdline), "\x00", " ")
	return strings.Contains(text, "runtime-bootstrap") && !strings.Contains(text, containerdChildArg)
}

func stopOrphanedEmbeddedContainerdFromProc() error {
	pids, err := orphanContainerdChildPIDs()
	if err != nil {
		return err
	}
	if len(pids) == 0 {
		return nil
	}
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	deadline := time.Now().Add(orphanStopGrace)
	for time.Now().Before(deadline) {
		remaining, err := orphanContainerdChildPIDs()
		if err != nil {
			return err
		}
		if len(remaining) == 0 {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	return nil
}

func orphanContainerdChildPIDs() ([]int, error) {
	pids, err := findContainerdChildPIDs()
	if err != nil {
		return nil, err
	}
	live := currentLiveContainerdChildPID()
	if live <= 0 || len(pids) == 0 {
		return pids, nil
	}
	out := make([]int, 0, len(pids))
	for _, pid := range pids {
		if pid == live {
			continue
		}
		out = append(out, pid)
	}
	return out, nil
}

func findContainerdChildPIDs() ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("read /proc: %w", err)
	}
	var pids []int
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil || pid <= 1 {
			continue
		}
		cmdline, readErr := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if readErr != nil {
			continue
		}
		text := strings.ReplaceAll(string(cmdline), "\x00", " ")
		if strings.Contains(text, containerdChildArg) {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}
