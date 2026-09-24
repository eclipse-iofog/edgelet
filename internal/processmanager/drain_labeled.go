package processmanager

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// LabeledWorkloadStopTimeoutSec is the CRI StopContainer SIGTERM budget for
// labeled workloads (longer than the runtime 10s default so flocked VOLUME
// mounts can exit cleanly). Stops stay concurrent and must fit the 120s
// data-plane stop budget.
const LabeledWorkloadStopTimeoutSec int64 = 30

const defaultLabeledWorkloadStopTimeoutSec = LabeledWorkloadStopTimeoutSec

// LabeledWorkloadRuntime lists and stops labeled Edgelet workloads while the
// container runtime is still up. Control-plane API access is not required.
type LabeledWorkloadRuntime interface {
	ListLabeledRunning(ctx context.Context) ([]string, error)
	StopContainer(ctx context.Context, id string, timeoutSec int64) error
}

// DrainLabeledWorkloads issues concurrent CRI StopContainer calls for labeled
// running workloads and returns when the set is empty or the budget expires.
func DrainLabeledWorkloads(ctx context.Context, runtime LabeledWorkloadRuntime, timeout time.Duration, stopTimeoutSec int64) error {
	if runtime == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if stopTimeoutSec <= 0 {
		stopTimeoutSec = defaultLabeledWorkloadStopTimeoutSec
	}

	startedAt := time.Now()
	initialIDs, err := runtime.ListLabeledRunning(ctx)
	if err != nil {
		return fmt.Errorf("list labeled workloads during data-plane drain: %w", err)
	}
	if timeout <= 0 {
		timeout = adaptiveShutdownDrainTimeout(len(initialIDs))
	}
	deadline := startedAt.Add(timeout)

	if len(initialIDs) == 0 {
		return nil
	}

	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("data-plane drain canceled: %w", err)
		}

		runtimeIDs, err := runtime.ListLabeledRunning(ctx)
		if err != nil {
			return fmt.Errorf("list labeled workloads during data-plane drain: %w", err)
		}
		if len(runtimeIDs) == 0 {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf(
				"timed out draining labeled workloads after %s; remaining container IDs: %s",
				timeout,
				strings.Join(runtimeIDs, ","),
			)
		}

		stopTimeout := stopTimeoutForBudget(stopTimeoutSec, deadline)
		stopLabeledWorkloadsConcurrently(ctx, runtime, runtimeIDs, stopTimeout)

		runtimeIDs, err = runtime.ListLabeledRunning(ctx)
		if err != nil {
			return fmt.Errorf("list labeled workloads during data-plane drain: %w", err)
		}
		if len(runtimeIDs) == 0 {
			return nil
		}

		sleep := shutdownDrainPollInterval
		if remaining := time.Until(deadline); remaining < sleep {
			if remaining <= 0 {
				continue
			}
			sleep = remaining
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("data-plane drain canceled: %w", ctx.Err())
		case <-time.After(sleep):
		}
	}
}

func stopTimeoutForBudget(stopTimeoutSec int64, deadline time.Time) int64 {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 0
	}
	if stopTimeoutSec <= 0 {
		stopTimeoutSec = defaultLabeledWorkloadStopTimeoutSec
	}
	remainingSec := int64((remaining + time.Second - 1) / time.Second)
	if remainingSec < stopTimeoutSec {
		return remainingSec
	}
	return stopTimeoutSec
}

func stopLabeledWorkloadsConcurrently(ctx context.Context, runtime LabeledWorkloadRuntime, containerIDs []string, stopTimeoutSec int64) {
	if len(containerIDs) == 0 || runtime == nil {
		return
	}
	workerCount := shutdownDrainWorkerCount(len(containerIDs))
	jobs := make(chan string)
	var wg sync.WaitGroup

	worker := func() {
		defer wg.Done()
		for containerID := range jobs {
			if ctx.Err() != nil {
				return
			}
			_ = runtime.StopContainer(ctx, containerID, stopTimeoutSec)
		}
	}

	wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go worker()
	}
	for _, id := range containerIDs {
		jobs <- id
	}
	close(jobs)
	wg.Wait()
}

// IsLabeledWorkloadDrainTimeout reports whether drain stopped because the budget expired.
func IsLabeledWorkloadDrainTimeout(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "timed out draining labeled workloads")
}
