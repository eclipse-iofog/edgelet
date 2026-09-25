package processmanager

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestQuiesceLabeledWorkloads_SIGTERMExitVerifyPass(t *testing.T) {
	fake := newLabeledDrainFake("c1")
	var killed []int
	result := QuiesceLabeledWorkloads(context.Background(), fake, time.Second, QuiesceDeps{
		StopTimeoutSec: LabeledWorkloadStopTimeoutSec,
		AfterKillWait:  time.Millisecond,
		VolumeHolders:  func(string) ([]int, error) { return nil, nil },
		ListTasks:      func(context.Context) ([]string, error) { return nil, nil },
		ListPIDs:       func(context.Context) ([]int, error) { return nil, nil },
		Kill: func(pid int) error {
			killed = append(killed, pid)
			return nil
		},
	})
	if result.Status != DrainQuiesceComplete || !result.Verified() {
		t.Fatalf("expected complete verify after SIGTERM exit, got %+v", result)
	}
	if got := atomic.LoadInt64(&fake.stopCalls); got == 0 {
		t.Fatal("expected CRI StopContainer (SIGTERM) before verify")
	}
	if len(killed) != 0 {
		t.Fatalf("expected no leftover SIGKILL after clean SIGTERM exit, got %v", killed)
	}
}

func TestQuiesceLabeledWorkloads_IgnoresSIGTERMThenSIGKILLReleasesHolder(t *testing.T) {
	fake := newLabeledDrainFake("c1")
	fake.stopRemoves = false

	var holders atomic.Value
	holders.Store([]int{4242})
	var killed []int
	result := QuiesceLabeledWorkloads(context.Background(), fake, 20*time.Millisecond, QuiesceDeps{
		DiskDirectory:  t.TempDir(),
		StopTimeoutSec: LabeledWorkloadStopTimeoutSec,
		AfterKillWait:  time.Millisecond,
		ListPIDs:       func(context.Context) ([]int, error) { return []int{4242}, nil },
		// CRI objects are already gone. Force-kill is only for the volume holder.
		ListTasks: func(context.Context) ([]string, error) { return nil, nil },
		VolumeHolders: func(string) ([]int, error) {
			if remaining, ok := holders.Load().([]int); ok {
				return remaining, nil
			}
			return nil, nil
		},
		Kill: func(pid int) error {
			killed = append(killed, pid)
			if pid == 4242 {
				holders.Store([]int(nil))
				fake.mu.Lock()
				delete(fake.running, "c1")
				fake.mu.Unlock()
			}
			return nil
		},
		SelfPID: 1,
	})
	if result.Status != DrainQuiesceComplete {
		t.Fatalf("expected verify pass after SIGKILL released holder, got %+v", result)
	}
	if len(killed) == 0 || killed[0] != 4242 {
		t.Fatalf("expected SIGKILL of leftover holder, got %v", killed)
	}
}

func TestQuiesceLabeledWorkloads_SkipsProtectedDataPlanePIDs(t *testing.T) {
	fake := newLabeledDrainFake("c1")
	var killed []int
	result := QuiesceLabeledWorkloads(context.Background(), fake, time.Second, QuiesceDeps{
		StopTimeoutSec: LabeledWorkloadStopTimeoutSec,
		AfterKillWait:  time.Millisecond,
		ListTasks:      func(context.Context) ([]string, error) { return nil, nil },
		ListPIDs:       func(context.Context) ([]int, error) { return []int{7, 8}, nil },
		VolumeHolders:  func(string) ([]int, error) { return []int{7}, nil },
		ProtectPID: func(pid int) bool {
			return pid == 7
		},
		Kill: func(pid int) error {
			killed = append(killed, pid)
			return nil
		},
		SelfPID: 1,
	})
	if result.Status != DrainQuiesceComplete {
		t.Fatalf("protected holder must not fail verify, got %+v", result)
	}
	if len(killed) != 1 || killed[0] != 8 {
		t.Fatalf("expected SIGKILL of only the unprotected pid, got %v", killed)
	}
}

func TestQuiesceLabeledWorkloads_ExitedContainerSkipsForce(t *testing.T) {
	fake := newLabeledDrainFake("c1")
	var killed []int
	released := false
	result := QuiesceLabeledWorkloads(context.Background(), fake, time.Second, QuiesceDeps{
		AfterKillWait: time.Millisecond,
		ListTasks:     func(context.Context) ([]string, error) { return []string{"c1"}, nil },
		VolumeHolders: func(string) ([]int, error) { return []int{77}, nil },
		Release: func(context.Context) error {
			released = true
			return nil
		},
		Kill: func(pid int) error {
			killed = append(killed, pid)
			return nil
		},
	})
	if result.Status != DrainQuiesceVerifyFailed {
		t.Fatalf("expected verifyFailed while a labeled container remains, got %+v", result)
	}
	if !released {
		t.Fatal("expected container and sandbox release before verify")
	}
	if len(killed) != 0 {
		t.Fatalf("expected no SIGKILL while a CRI object remains, got %v", killed)
	}
}

func TestQuiesceLabeledWorkloads_ReleaseErrorVerifyFails(t *testing.T) {
	fake := newLabeledDrainFake("c1")
	result := QuiesceLabeledWorkloads(context.Background(), fake, time.Second, QuiesceDeps{
		AfterKillWait: time.Millisecond,
		ListTasks:     func(context.Context) ([]string, error) { return nil, nil },
		VolumeHolders: func(string) ([]int, error) { return nil, nil },
		Release:       func(context.Context) error { return errors.New("remove sandbox failed") },
	})
	if result.Status != DrainQuiesceVerifyFailed {
		t.Fatalf("expected verifyFailed when runtime delete fails, got %+v", result)
	}
}

func TestQuiesceLabeledWorkloads_UnkillableHolderVerifyFails(t *testing.T) {
	fake := newLabeledDrainFake("c1")
	result := QuiesceLabeledWorkloads(context.Background(), fake, 20*time.Millisecond, QuiesceDeps{
		AfterKillWait: time.Millisecond,
		ListPIDs:      func(context.Context) ([]int, error) { return []int{99}, nil },
		ListTasks:     func(context.Context) ([]string, error) { return nil, nil },
		VolumeHolders: func(string) ([]int, error) { return []int{99}, nil },
		Kill:          func(int) error { return errors.New("unkillable") },
		SelfPID:       1,
	})
	if result.Status != DrainQuiesceVerifyFailed {
		t.Fatalf("expected verifyFailed for unkillable holder, got %+v", result)
	}
}

func TestQuiesceLabeledWorkloads_UsesLongerStopTimeout(t *testing.T) {
	fake := newLabeledDrainFake("c1")
	var seen int64
	fake.recordStopTimeout = &seen
	result := QuiesceLabeledWorkloads(context.Background(), fake, time.Minute, QuiesceDeps{
		AfterKillWait: time.Millisecond,
		VolumeHolders: func(string) ([]int, error) { return nil, nil },
		ListTasks:     func(context.Context) ([]string, error) { return nil, nil },
	})
	if result.Status != DrainQuiesceComplete {
		t.Fatalf("expected complete, got %+v", result)
	}
	if got := atomic.LoadInt64(&seen); got != LabeledWorkloadStopTimeoutSec {
		t.Fatalf("expected StopContainer timeout %d, got %d", LabeledWorkloadStopTimeoutSec, got)
	}
}

func TestQuiesceLabeledWorkloads_RetryStopsAtDeadline(t *testing.T) {
	fake := newLabeledDrainFake()
	start := time.Now()
	result := QuiesceLabeledWorkloads(context.Background(), fake, 80*time.Millisecond, QuiesceDeps{
		AfterKillWait: time.Millisecond,
		Poll:          time.Second,
		VolumeHolders: func(string) ([]int, error) { return nil, nil },
		ListTasks:     func(context.Context) ([]string, error) { return []string{"sandbox-1"}, nil },
		Release:       func(context.Context) error { return nil },
	})
	elapsed := time.Since(start)
	if result.Status != DrainQuiesceVerifyFailed {
		t.Fatalf("expected verifyFailed at deadline, got %+v", result)
	}
	if elapsed > 400*time.Millisecond {
		t.Fatalf("retry ran past the quiesce deadline: %s", elapsed)
	}
}

func TestQuiesceLabeledWorkloads_ResidueClearsOnRetry(t *testing.T) {
	fake := newLabeledDrainFake()
	var calls atomic.Int32
	result := QuiesceLabeledWorkloads(context.Background(), fake, 2*time.Second, QuiesceDeps{
		AfterKillWait: time.Millisecond,
		Poll:          10 * time.Millisecond,
		VolumeHolders: func(string) ([]int, error) { return nil, nil },
		Release:       func(context.Context) error { return nil },
		ListTasks: func(context.Context) ([]string, error) {
			if calls.Add(1) == 1 {
				return []string{"sandbox-1"}, nil
			}
			return nil, nil
		},
	})
	if result.Status != DrainQuiesceComplete {
		t.Fatalf("expected complete after residue cleared, got %+v", result)
	}
}

func TestQuiesceLabeledWorkloads_ListErrorClearsOnRetry(t *testing.T) {
	inner := newLabeledDrainFake()
	flaky := &flakyListRuntime{inner: inner, fails: 1}
	result := QuiesceLabeledWorkloads(context.Background(), flaky, 2*time.Second, QuiesceDeps{
		AfterKillWait: time.Millisecond,
		Poll:          10 * time.Millisecond,
		VolumeHolders: func(string) ([]int, error) { return nil, nil },
		ListTasks:     func(context.Context) ([]string, error) { return nil, nil },
	})
	if result.Status != DrainQuiesceComplete {
		t.Fatalf("expected complete after list recovered, got %+v", result)
	}
}

func TestQuiesceLabeledWorkloads_ReleaseUsesRemainingStopBudget(t *testing.T) {
	fake := newLabeledDrainFake()
	remaining := &atomic.Int64{}
	var seen atomic.Int64
	var lists atomic.Int32
	result := QuiesceLabeledWorkloads(context.Background(), fake, 3*time.Second, QuiesceDeps{
		StopTimeoutSec: 30,
		RemainingStop:  remaining,
		AfterKillWait:  time.Millisecond,
		Poll:           10 * time.Millisecond,
		VolumeHolders:  func(string) ([]int, error) { return nil, nil },
		Release: func(context.Context) error {
			seen.Store(remaining.Load())
			return nil
		},
		ListTasks: func(context.Context) ([]string, error) {
			if lists.Add(1) == 1 {
				return []string{"sandbox-1"}, nil
			}
			return nil, nil
		},
	})
	if result.Status != DrainQuiesceComplete {
		t.Fatalf("expected complete, got %+v", result)
	}
	if got := seen.Load(); got <= 0 || got > 3 {
		t.Fatalf("stop budget = %d, want the time left on the 3s deadline", got)
	}
}

func TestQuiesceLabeledWorkloads_ReleaseErrorClearsOnRetry(t *testing.T) {
	fake := newLabeledDrainFake()
	var calls atomic.Int32
	result := QuiesceLabeledWorkloads(context.Background(), fake, 2*time.Second, QuiesceDeps{
		AfterKillWait: time.Millisecond,
		Poll:          10 * time.Millisecond,
		VolumeHolders: func(string) ([]int, error) { return nil, nil },
		ListTasks:     func(context.Context) ([]string, error) { return nil, nil },
		Release: func(context.Context) error {
			if calls.Add(1) == 1 {
				return errors.New("remove sandbox failed")
			}
			return nil
		},
	})
	if result.Status != DrainQuiesceComplete {
		t.Fatalf("expected complete after delete recovered, got %+v", result)
	}
}

func TestQuiesceLabeledWorkloads_VolumeHolderDoesNotRetry(t *testing.T) {
	fake := newLabeledDrainFake("c1")
	start := time.Now()
	result := QuiesceLabeledWorkloads(context.Background(), fake, 3*time.Second, QuiesceDeps{
		AfterKillWait: time.Millisecond,
		ListTasks:     func(context.Context) ([]string, error) { return nil, nil },
		VolumeHolders: func(string) ([]int, error) { return []int{99}, nil },
		Kill:          func(int) error { return errors.New("unkillable") },
		SelfPID:       1,
	})
	if result.Status != DrainQuiesceVerifyFailed {
		t.Fatalf("expected verifyFailed for holder, got %+v", result)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("volume holder should fail closed without using the grace budget, took %s", elapsed)
	}
}

type flakyListRuntime struct {
	inner *labeledDrainFake
	fails int
}

func (f *flakyListRuntime) ListLabeledRunning(ctx context.Context) ([]string, error) {
	if f.fails > 0 {
		f.fails--
		return nil, errors.New("cri unavailable")
	}
	return f.inner.ListLabeledRunning(ctx)
}

func (f *flakyListRuntime) StopContainer(ctx context.Context, id string, timeoutSec int64) error {
	return f.inner.StopContainer(ctx, id, timeoutSec)
}

func TestQuiesceLabeledWorkloads_RuntimeListErrorTimesOutWhenClear(t *testing.T) {
	fake := newLabeledDrainFake()
	fake.listErr = errors.New("cri unavailable")
	result := QuiesceLabeledWorkloads(context.Background(), fake, time.Second, QuiesceDeps{
		AfterKillWait: time.Millisecond,
		VolumeHolders: func(string) ([]int, error) { return nil, nil },
		ListTasks:     func(context.Context) ([]string, error) { return nil, nil },
	})
	if result.Status != DrainQuiesceTimedOut {
		t.Fatalf("expected timedOut when runtime is down but trees are clear, got %+v", result)
	}
}
