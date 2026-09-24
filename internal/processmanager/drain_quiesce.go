package processmanager

import (
	"context"
	"os"
	"time"
)

const (
	// DrainQuiesceComplete means grace/force finished and verify passed.
	DrainQuiesceComplete = "complete"
	// DrainQuiesceTimedOut means the runtime was unreachable or drain could not finish.
	DrainQuiesceTimedOut = "timedOut"
	// DrainQuiesceVerifyFailed means leftovers remained after SIGKILL.
	DrainQuiesceVerifyFailed = "verifyFailed"
)

const leftoverKillWait = 2 * time.Second

// DrainQuiesceResult is the programmatic outcome of grace → force → verify.
type DrainQuiesceResult struct {
	Status string
}

// Verified reports whether drain+verify completed and shim reap may proceed.
func (r DrainQuiesceResult) Verified() bool {
	return r.Status == DrainQuiesceComplete
}

// QuiesceDeps customizes leftover PID discovery and signals (tests inject fakes).
type QuiesceDeps struct {
	DiskDirectory  string
	StopTimeoutSec int64
	ListPIDs       func(ctx context.Context) ([]int, error)
	ListTasks      func(ctx context.Context) ([]string, error)
	VolumeHolders  func(diskDirectory string) ([]int, error)
	// Release deletes labeled containers and pod sandboxes after SIGTERM.
	// Force-kill runs only when Release leaves no CRI object behind.
	Release func(ctx context.Context) error
	Kill    func(pid int) error
	SelfPID int
	// ProtectPID is true for processes that must survive force-kill: the
	// data-plane parent, the live containerd child, and shims still attached
	// to that child's socket.
	ProtectPID    func(pid int) bool
	AfterKillWait time.Duration
}

// QuiesceLabeledWorkloads runs CRI SIGTERM, deletes labeled containers and pod
// sandboxes, then SIGKILLs volume-tree holders that survived that delete.
// An exited container or a remaining sandbox is not clear: force-kill and shim
// reap must not run while containerd still owns the task.
func QuiesceLabeledWorkloads(ctx context.Context, runtime LabeledWorkloadRuntime, timeout time.Duration, deps QuiesceDeps) DrainQuiesceResult {
	if ctx == nil {
		ctx = context.Background()
	}
	deps = deps.withDefaults(runtime)

	graceErr := DrainLabeledWorkloads(ctx, runtime, timeout, deps.StopTimeoutSec)
	if graceErr != nil && !IsLabeledWorkloadDrainTimeout(graceErr) && runtime != nil {
		// Runtime list/stop failed. Still force volume holders, then fail closed
		// unless verify can prove the node is clear.
		forceVolumeHolders(deps)
		if verifyErr := verifyDataPlaneClear(ctx, deps); verifyErr != nil {
			return DrainQuiesceResult{Status: DrainQuiesceVerifyFailed}
		}
		return DrainQuiesceResult{Status: DrainQuiesceTimedOut}
	}

	if deps.Release != nil {
		if err := deps.Release(ctx); err != nil {
			return DrainQuiesceResult{Status: DrainQuiesceVerifyFailed}
		}
	}
	if blocked, err := labeledTasksRemain(ctx, deps); err != nil || blocked {
		return DrainQuiesceResult{Status: DrainQuiesceVerifyFailed}
	}

	forceLeftovers(ctx, deps)
	if verifyErr := verifyDataPlaneClear(ctx, deps); verifyErr != nil {
		return DrainQuiesceResult{Status: DrainQuiesceVerifyFailed}
	}
	return DrainQuiesceResult{Status: DrainQuiesceComplete}
}

// labeledTasksRemain reports whether containerd still has a labeled container or sandbox.
// Force-kill is skipped while any remain, including CONTAINER_EXITED.
func labeledTasksRemain(ctx context.Context, deps QuiesceDeps) (bool, error) {
	if deps.ListTasks == nil {
		return false, errLabeledTasksUnknown
	}
	tasks, err := deps.ListTasks(ctx)
	if err != nil {
		return false, err
	}
	return len(tasks) > 0, nil
}

func (d QuiesceDeps) withDefaults(runtime LabeledWorkloadRuntime) QuiesceDeps {
	if d.StopTimeoutSec <= 0 {
		d.StopTimeoutSec = LabeledWorkloadStopTimeoutSec
	}
	if d.VolumeHolders == nil {
		d.VolumeHolders = ListVolumeTreeHolders
	}
	if d.Kill == nil {
		d.Kill = killProcess
	}
	if d.SelfPID <= 0 {
		d.SelfPID = os.Getpid()
	}
	if d.AfterKillWait < 0 {
		d.AfterKillWait = 0
	}
	if d.AfterKillWait == 0 {
		d.AfterKillWait = leftoverKillWait
	}
	if d.ListTasks == nil && runtime != nil {
		d.ListTasks = func(ctx context.Context) ([]string, error) {
			return runtime.ListLabeledRunning(ctx)
		}
	}
	return d
}

func forceLeftovers(ctx context.Context, deps QuiesceDeps) {
	pids := collectForcePIDs(ctx, deps)
	killPIDs(pids, deps)
	waitHoldersGone(deps)
}

func forceVolumeHolders(deps QuiesceDeps) {
	if deps.VolumeHolders == nil {
		return
	}
	holders, err := deps.VolumeHolders(deps.DiskDirectory)
	if err != nil {
		return
	}
	killPIDs(holders, deps)
	waitHoldersGone(deps)
}

func collectForcePIDs(ctx context.Context, deps QuiesceDeps) []int {
	var pids []int
	if deps.ListPIDs != nil {
		if listed, err := deps.ListPIDs(ctx); err == nil {
			pids = append(pids, listed...)
		}
	}
	if deps.VolumeHolders != nil {
		if holders, err := deps.VolumeHolders(deps.DiskDirectory); err == nil {
			pids = append(pids, holders...)
		}
	}
	return uniquePIDs(pids)
}

func killPIDs(pids []int, deps QuiesceDeps) {
	if deps.Kill == nil {
		return
	}
	seen := make(map[int]struct{}, len(pids))
	for _, pid := range pids {
		if deps.sparePID(pid) {
			continue
		}
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		_ = deps.Kill(pid)
	}
}

func waitHoldersGone(deps QuiesceDeps) {
	if deps.AfterKillWait <= 0 || deps.VolumeHolders == nil {
		return
	}
	deadline := time.Now().Add(deps.AfterKillWait)
	for {
		holders, err := deps.VolumeHolders(deps.DiskDirectory)
		if err == nil && len(deps.killablePIDs(holders)) == 0 {
			return
		}
		if !time.Now().Before(deadline) {
			return
		}
		sleep := 50 * time.Millisecond
		if remaining := time.Until(deadline); remaining < sleep {
			if remaining <= 0 {
				return
			}
			sleep = remaining
		}
		time.Sleep(sleep)
	}
}

func verifyDataPlaneClear(ctx context.Context, deps QuiesceDeps) error {
	if deps.VolumeHolders != nil {
		holders, err := deps.VolumeHolders(deps.DiskDirectory)
		if err != nil {
			return err
		}
		if len(deps.killablePIDs(holders)) > 0 {
			return errVolumeHoldersRemain
		}
	}
	if deps.ListTasks == nil {
		return errLabeledTasksUnknown
	}
	tasks, err := deps.ListTasks(ctx)
	if err != nil {
		return err
	}
	if len(tasks) > 0 {
		return errLabeledTasksRemain
	}
	return nil
}

func uniquePIDs(pids []int) []int {
	if len(pids) == 0 {
		return nil
	}
	seen := make(map[int]struct{}, len(pids))
	out := make([]int, 0, len(pids))
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		out = append(out, pid)
	}
	return out
}

func (d QuiesceDeps) sparePID(pid int) bool {
	if pid <= 1 || pid == d.SelfPID {
		return true
	}
	return d.ProtectPID != nil && d.ProtectPID(pid)
}

func (d QuiesceDeps) killablePIDs(pids []int) []int {
	if len(pids) == 0 {
		return nil
	}
	out := make([]int, 0, len(pids))
	for _, pid := range pids {
		if d.sparePID(pid) {
			continue
		}
		out = append(out, pid)
	}
	return out
}
