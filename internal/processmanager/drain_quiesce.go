package processmanager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
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
	// RemainingStop is the CRI stop budget still left on the quiesce deadline.
	// Release reads it so a retry does not start a fresh stop timeout.
	RemainingStop *atomic.Int64
	// Poll is the delay between CRI delete retries. Zero uses the drain poll interval.
	Poll time.Duration
	// Sleep and Now override the clock in tests.
	Sleep func(time.Duration)
	Now   func() time.Time
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
	started := deps.Now()
	deadline := started.Add(timeout)
	if timeout <= 0 {
		deadline = started
	}

	var graceErr, releaseErr error
	var residue []string
	attempted := false

	for {
		if attempted && !deps.Now().Before(deadline) {
			return finishQuiesce(ctx, deps, graceErr, releaseErr, residue)
		}
		attempted = true

		stopSec := stopTimeoutForBudget(deps.StopTimeoutSec, deadline)
		deps.setStopBudget(stopSec)
		remaining := deadline.Sub(deps.Now())
		if remaining < 0 {
			remaining = 0
		}
		// A zero budget must not be passed through: the grace helper treats
		// that as a fresh adaptive stop, and release treats it as one second.
		if remaining <= 0 || stopSec <= 0 {
			graceErr = fmt.Errorf("timed out draining labeled workloads after %s", timeout)
		} else {
			graceErr = DrainLabeledWorkloads(ctx, runtime, remaining, stopSec)
		}

		if graceErr != nil && !IsLabeledWorkloadDrainTimeout(graceErr) && runtime != nil {
			// List or stop failed. A volume holder still fails closed immediately.
			// A CRI error is retried until the same deadline.
			forceVolumeHolders(deps)
			if holdErr := volumeHoldersRemain(deps); holdErr != nil {
				logQuiesceVerifyFailed(graceErr, releaseErr, nil)
				return DrainQuiesceResult{Status: DrainQuiesceVerifyFailed}
			}
		} else {
			stopSec = stopTimeoutForBudget(deps.StopTimeoutSec, deadline)
			deps.setStopBudget(stopSec)
			releaseErr = nil
			if deps.Release != nil && stopSec > 0 {
				releaseErr = deps.Release(ctx)
			}
			var taskErr error
			residue, taskErr = labeledResidue(ctx, deps)
			if taskErr != nil && graceErr == nil {
				graceErr = taskErr
			}
			if releaseErr == nil && taskErr == nil && len(residue) == 0 {
				forceLeftovers(ctx, deps)
				if holdErr := volumeHoldersRemain(deps); holdErr != nil {
					logQuiesceVerifyFailed(graceErr, nil, nil)
					return DrainQuiesceResult{Status: DrainQuiesceVerifyFailed}
				}
				if taskErr = verifyDataPlaneClear(ctx, deps); taskErr != nil {
					if isVolumeHolderErr(taskErr) {
						logQuiesceVerifyFailed(graceErr, nil, nil)
						return DrainQuiesceResult{Status: DrainQuiesceVerifyFailed}
					}
					residue, _ = labeledResidue(ctx, deps)
				} else {
					return DrainQuiesceResult{Status: DrainQuiesceComplete}
				}
			}
		}

		if !deps.Now().Before(deadline) {
			return finishQuiesce(ctx, deps, graceErr, releaseErr, residue)
		}
		wait := deps.Poll
		if left := deadline.Sub(deps.Now()); left < wait {
			wait = left
		}
		if wait > 0 {
			deps.Sleep(wait)
		}
	}
}

func finishQuiesce(ctx context.Context, deps QuiesceDeps, graceErr, releaseErr error, residue []string) DrainQuiesceResult {
	if holdErr := volumeHoldersRemain(deps); holdErr != nil {
		logQuiesceVerifyFailed(graceErr, releaseErr, residue)
		return DrainQuiesceResult{Status: DrainQuiesceVerifyFailed}
	}
	if residue == nil {
		residue, _ = labeledResidue(ctx, deps)
	}
	if releaseErr != nil || len(residue) > 0 {
		logQuiesceVerifyFailed(graceErr, releaseErr, residue)
		return DrainQuiesceResult{Status: DrainQuiesceVerifyFailed}
	}
	if graceErr != nil && !IsLabeledWorkloadDrainTimeout(graceErr) {
		return DrainQuiesceResult{Status: DrainQuiesceTimedOut}
	}
	logQuiesceVerifyFailed(graceErr, releaseErr, residue)
	return DrainQuiesceResult{Status: DrainQuiesceVerifyFailed}
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
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Sleep == nil {
		d.Sleep = time.Sleep
	}
	if d.Poll <= 0 {
		d.Poll = shutdownDrainPollInterval
	}
	return d
}

func (d QuiesceDeps) setStopBudget(sec int64) {
	if d.RemainingStop != nil {
		d.RemainingStop.Store(sec)
	}
}

func labeledResidue(ctx context.Context, deps QuiesceDeps) ([]string, error) {
	if deps.ListTasks == nil {
		return nil, errLabeledTasksUnknown
	}
	tasks, err := deps.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	return tasks, nil
}

func volumeHoldersRemain(deps QuiesceDeps) error {
	if deps.VolumeHolders == nil {
		return nil
	}
	holders, err := deps.VolumeHolders(deps.DiskDirectory)
	if err != nil {
		return err
	}
	if len(deps.killablePIDs(holders)) > 0 {
		return errVolumeHoldersRemain
	}
	return nil
}

func isVolumeHolderErr(err error) bool {
	return errors.Is(err, errVolumeHoldersRemain)
}

func logQuiesceVerifyFailed(graceErr, releaseErr error, residue []string) {
	logging.LogWarn(ProcessManagerModuleName, fmt.Sprintf(
		"data-plane drain verify failed grace=%s release=%s %s",
		quiesceErrText(graceErr), quiesceErrText(releaseErr), residueSample(residue),
	))
}

func quiesceErrText(err error) string {
	if err == nil {
		return "none"
	}
	return err.Error()
}

func residueSample(ids []string) string {
	const maxIDs = 8
	n := len(ids)
	sample := ids
	if n > maxIDs {
		sample = ids[:maxIDs]
	}
	return fmt.Sprintf("residue count=%d ids=%s", n, strings.Join(sample, ","))
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
