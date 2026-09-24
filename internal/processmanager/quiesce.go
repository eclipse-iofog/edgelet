package processmanager

import (
	"context"
	"errors"
	"sync"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/runtimestate"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/eclipse-iofog/edgelet/pkg/engine"
)

var (
	quiesceMu                 sync.RWMutex
	quiesced                  bool
	quiescedForDataPlaneDrain bool
)

// SetQuiesced blocks or resumes reconcile scheduling.
func SetQuiesced(v bool) {
	quiesceMu.Lock()
	quiesced = v
	if !v {
		quiescedForDataPlaneDrain = false
	}
	quiesceMu.Unlock()
}

// IsQuiesced reports whether reconcile is paused (e.g. pending engine restart).
func IsQuiesced() bool {
	quiesceMu.RLock()
	defer quiesceMu.RUnlock()
	return quiesced
}

// RuntimeEngineAvailable reports whether the process manager has a wired container engine.
func RuntimeEngineAvailable() bool {
	pm := GetInstance()
	return pm != nil && pm.engine != nil
}

// IsQuiescedForDataPlaneDrain reports whether reconcile is held for a data-plane stop
// until the runtime engine socket becomes healthy again.
func IsQuiescedForDataPlaneDrain() bool {
	quiesceMu.RLock()
	defer quiesceMu.RUnlock()
	return quiescedForDataPlaneDrain
}

// BeginQuiesceForDataPlaneDrain pauses reconcile for coordinated data-plane MS drain.
// Reconcile resumes via TryResumeReconcileAfterDataPlaneEngineReady once the engine is ready.
func BeginQuiesceForDataPlaneDrain() {
	quiesceMu.Lock()
	quiesced = true
	quiescedForDataPlaneDrain = true
	quiesceMu.Unlock()
}

// TryResumeReconcileAfterDataPlaneEngineReady clears data-plane drain quiesce after the
// runtime engine is healthy again (runtime split attach path).
// A drain-active file keeps the pause until the data plane clears it.
func TryResumeReconcileAfterDataPlaneEngineReady() {
	if DataPlaneDrainHoldActive() {
		BeginQuiesceForDataPlaneDrain()
		runtimestate.GetState().SetEngineReady(false)
		return
	}
	quiesceMu.Lock()
	if !quiescedForDataPlaneDrain {
		quiesceMu.Unlock()
		return
	}
	quiescedForDataPlaneDrain = false
	quiesced = false
	runtimestate.GetState().SetEngineReady(true)
	pm := GetInstance()
	quiesceMu.Unlock()

	logging.LogInfo(ProcessManagerModuleName, "engine_ready_resume")
	if pm != nil {
		pm.notifyMonitorThread()
	}
}

// SetEngine swaps the active container engine without restarting the process manager.
func (pm *ProcessManager) SetEngine(eng engine.ContainerEngine, engineName string) {
	pm.engine = eng
	pm.engineName = engineName
	if pm.containerManager != nil {
		pm.containerManager.engine = eng
		pm.containerManager.engineName = engineName
	}
}

// CleanupForEngineSwitch stops/removes all managed workload containers and clears ephemeral DB state.
func (pm *ProcessManager) CleanupForEngineSwitch(ctx context.Context) error {
	if pm == nil {
		return errors.New("process manager is nil")
	}

	pm.logger.Info("Cleaning up microservice runtime state for container engine change")

	if pm.engine != nil && pm.containerManager != nil {
		if pm.microserviceManager != nil {
			for _, ms := range pm.microserviceManager.GetLatestMicroservices() {
				if ms == nil || ms.Delete {
					continue
				}
				_ = pm.containerManager.RemoveContainerByMicroserviceUUID(ctx, ms.MicroserviceUUID, false, false)
			}
		}
		if items, err := store.GetInstance().ListLocalWorkloads(); err == nil {
			for _, item := range items {
				if item == nil {
					continue
				}
				if item.ContainerID != "" {
					_ = pm.containerManager.RemoveContainerRuntimeForEngineSwitch(ctx, item.ContainerID)
					continue
				}
				if container, err := pm.containerManager.GetContainerForMicroservice(item.LocalUUID); err == nil && container != nil {
					_ = pm.containerManager.RemoveContainerRuntimeForEngineSwitch(ctx, container.ID)
				}
			}
		}
	}

	db := store.GetInstance()
	if err := db.ClearRuntimeContainerRefs(""); err != nil {
		return err
	}
	if err := db.ClearControllerMicroserviceRuntimeFields(); err != nil {
		return err
	}
	if err := db.ClearLocalWorkloadRuntimeFields(); err != nil {
		return err
	}

	statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
		if s == nil {
			return
		}
		s.ClearMicroserviceStatuses()
	})

	pm.notifyMonitorThread()
	return nil
}
