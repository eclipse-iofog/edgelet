package processmanager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/runtimeops"
	"github.com/eclipse-iofog/edgelet/internal/runtimestate"
)

// DrainRuntimeForDataPlaneStop drains labeled ioFog microservice containers before
// the embedded data-plane containerd stops. Control-plane attach-only stop must
// not call this path (see supervisor shouldDrainRuntimeOnControlStop).
//
// Reconcile is quiesced for the drain and stays quiesced until the runtime engine
// socket is healthy again (see TryResumeReconcileAfterDataPlaneEngineReady).
func DrainRuntimeForDataPlaneStop(timeout time.Duration) error {
	pm := GetInstance()
	if pm == nil {
		return nil
	}
	BeginQuiesceForDataPlaneDrain()
	err := pm.DrainRuntimeForDataPlaneShutdown(timeout)
	runtimestate.GetState().SetEngineReady(false)
	return err
}

func (pm *ProcessManager) ensureContainerManagerForDrain() {
	if pm == nil || pm.engine == nil || pm.containerManager != nil {
		return
	}
	pm.containerManager = NewContainerManager(pm.engine, pm.microserviceManager, pm.engineName)
	pm.containerManager.catalogDiskDirectory = pm.catalogDiskDirectory
}

func (pm *ProcessManager) teardownRuntimeWorkloadForDataPlaneShutdown(initialRuntimeIDs []string, timeout time.Duration) error {
	pm.ensureContainerManagerForDrain()
	if pm.containerManager == nil {
		return errors.New("data-plane drain: container manager unavailable")
	}

	startedAt := time.Now()
	targetSet := make(map[string]struct{}, len(initialRuntimeIDs))
	for _, id := range initialRuntimeIDs {
		targetSet[id] = struct{}{}
	}
	if timeout <= 0 {
		timeout = adaptiveShutdownDrainTimeout(len(initialRuntimeIDs))
	}
	deadline := startedAt.Add(timeout)

	if len(initialRuntimeIDs) == 0 {
		pm.emitShutdownDrain("data-plane runtime drain complete: no running workload containers", runtimeops.LevelInfo, "", map[string]any{
			"result":         runtimeops.ResultOK,
			"targetCount":    0,
			"stoppedCount":   0,
			"remainingCount": 0,
			"elapsedMs":      0,
		})
		return nil
	}

	ctx := context.Background()
	for {
		runtimeIDs, err := pm.runtimeWorkloadContainerIDs()
		if err != nil {
			return fmt.Errorf("list running containers during data-plane drain: %w", err)
		}

		removedCount := countStoppedTargets(targetSet, runtimeIDs)
		elapsedMs := time.Since(startedAt).Milliseconds()
		if len(runtimeIDs) == 0 {
			pm.emitShutdownDrain("data-plane runtime drain complete: no running workload containers", runtimeops.LevelInfo, "", map[string]any{
				"result":         runtimeops.ResultOK,
				"targetCount":    len(targetSet),
				"stoppedCount":   removedCount,
				"remainingCount": 0,
				"elapsedMs":      elapsedMs,
			})
			return nil
		}

		if time.Now().After(deadline) {
			remaining := strings.Join(runtimeIDs, ",")
			pm.emitShutdownDrain("data-plane runtime drain timed out", runtimeops.LevelError, runtimeops.ReasonShutdownDrainTimeout, map[string]any{
				"remainingContainerIds": remaining,
				"result":                runtimeops.ResultFailed,
				"targetCount":           len(targetSet),
				"stoppedCount":          removedCount,
				"remainingCount":        len(runtimeIDs),
				"elapsedMs":             elapsedMs,
			})
			return fmt.Errorf(
				"timed out draining runtime containers after %s; remaining container IDs: %s",
				timeout,
				remaining,
			)
		}

		pm.teardownRuntimeContainersConcurrently(ctx, runtimeIDs)
		time.Sleep(shutdownDrainPollInterval)
	}
}

func (pm *ProcessManager) teardownRuntimeContainersConcurrently(ctx context.Context, containerIDs []string) {
	if len(containerIDs) == 0 {
		return
	}
	workerCount := shutdownDrainWorkerCount(len(containerIDs))
	jobs := make(chan string)
	var wg sync.WaitGroup

	worker := func() {
		defer wg.Done()
		for containerID := range jobs {
			pm.teardownOneRuntimeContainerForDataPlaneDrain(ctx, containerID)
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

func (pm *ProcessManager) teardownOneRuntimeContainerForDataPlaneDrain(ctx context.Context, containerID string) {
	if pm.containerManager == nil {
		return
	}
	if err := pm.containerManager.RemoveContainerRuntimeForEngineSwitch(ctx, containerID); err != nil {
		pm.emitShutdownDrain("data-plane drain: runtime remove failed", runtimeops.LevelWarn, runtimeops.ReasonRemoveFailed, map[string]any{
			"containerId": containerID,
			"error":       err.Error(),
		})
	}
}
