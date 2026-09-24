package processmanager

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/controlplane"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/network"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/pkg/engine"
	"github.com/eclipse-iofog/edgelet/pkg/imageref"
)

// SetControlPlanePullOnRecreate requests an image pull on the next control plane generation recreate.
func (pm *ProcessManager) SetControlPlanePullOnRecreate(pull bool) {
	if pm == nil {
		return
	}
	pm.controlPlanePullOnRecreateMu.Lock()
	defer pm.controlPlanePullOnRecreateMu.Unlock()
	pm.controlPlanePullOnRecreate = pull
}

func (pm *ProcessManager) consumeControlPlanePullOnRecreate() bool {
	if pm == nil {
		return false
	}
	pm.controlPlanePullOnRecreateMu.Lock()
	defer pm.controlPlanePullOnRecreateMu.Unlock()
	pull := pm.controlPlanePullOnRecreate
	pm.controlPlanePullOnRecreate = false
	return pull
}

func (pm *ProcessManager) reconcileControlPlane() {
	item, found, err := store.GetInstance().GetSystemControlPlane()
	if err != nil {
		pm.logger.Warnf("control plane reconcile get deployment failed: %v", err)
		return
	}
	if !found || item == nil {
		return
	}

	nowSec := time.Now().Unix()
	item.NormalizeDefaults()
	item.LastReconcileAt = nowSec

	desired := strings.ToLower(strings.TrimSpace(item.DesiredState))
	if desired == "" {
		desired = "running"
	}

	container, err := pm.containerForControlPlane(item.ControllerUUID, item.ContainerID)
	if err != nil {
		item.LastError = err.Error()
		item.RuntimeState = "unknown"
		item.State = item.RuntimeState
		_ = store.GetInstance().UpsertSystemControlPlane(item)
		return
	}

	switch desired {
	case "stopped":
		pm.reconcileControlPlaneDesiredStopped(item, container, nowSec)
	case "deleted":
		pm.reconcileControlPlaneDesiredDeleted(item, container, nowSec)
	default:
		pm.reconcileControlPlaneDesiredRunning(item, container, nowSec)
	}
}

func (pm *ProcessManager) containerForControlPlane(controllerUUID, containerID string) (*engine.Container, error) {
	if pm.containerManager == nil {
		return nil, errors.New("process manager is not initialized")
	}
	if container, err := pm.containerManager.GetContainerForMicroservice(controllerUUID); err == nil && container != nil {
		return container, nil
	}
	if strings.TrimSpace(containerID) == "" || pm.engine == nil {
		return nil, nil
	}
	return pm.engine.GetContainerByID(containerID)
}

func (pm *ProcessManager) reconcileControlPlaneDesiredDeleted(item *models.ControlPlaneDeployment, container *engine.Container, now int64) {
	item.RuntimeState = "deleted"
	item.State = item.RuntimeState
	item.LastTransitionAt = now
	item.ObservedGeneration = item.Generation
	if item.DeletedAt == nil {
		ts := now
		item.DeletedAt = &ts
	}
	if container != nil {
		if err := pm.removeLocalContainerByID(container.ID); err != nil {
			item.LastError = err.Error()
			item.RuntimeState = "deleting"
			item.State = item.RuntimeState
			_ = store.GetInstance().UpsertSystemControlPlane(item)
			return
		}
	}
	item.ContainerID = ""
	item.FailureCount = 0
	_ = store.GetInstance().UpsertSystemControlPlane(item)
}

func (pm *ProcessManager) reconcileControlPlaneDesiredStopped(item *models.ControlPlaneDeployment, container *engine.Container, now int64) {
	item.ObservedGeneration = item.Generation
	item.LastTransitionAt = now
	if container != nil {
		if err := pm.StopMicroservice(item.ControllerUUID); err != nil {
			item.LastError = err.Error()
			item.RuntimeState = "stopping"
			item.State = item.RuntimeState
			_ = store.GetInstance().UpsertSystemControlPlane(item)
			return
		}
	}
	item.RuntimeState = "stopped"
	item.State = item.RuntimeState
	item.FailureCount = 0
	_ = store.GetInstance().UpsertSystemControlPlane(item)
}

func (pm *ProcessManager) reconcileControlPlaneDesiredRunning(item *models.ControlPlaneDeployment, container *engine.Container, now int64) {
	if container == nil {
		if controlPlaneLaunchInFlight(item, now) {
			pm.logger.Debugf(
				"Skipping control plane reconcile launch for %s: apply in-flight (generation=%d observed=%d)",
				item.ControllerUUID,
				item.Generation,
				item.ObservedGeneration,
			)
			return
		}
		pm.launchControlPlaneWithHook(item, now)
		return
	}

	if item.Generation > item.ObservedGeneration {
		pullImage := pm.consumeControlPlanePullOnRecreate()
		if err := pm.recreateControlPlaneDeployment(item, pullImage, now); err == nil || errors.Is(err, errVolumeInUse) {
			return
		}
	}

	status, err := pm.getLocalContainerStatus(container.ID, item.ControllerUUID)
	if err != nil {
		item.RuntimeState = "unknown"
		item.State = item.RuntimeState
		item.LastError = err.Error()
		item.LastTransitionAt = now
		_ = store.GetInstance().UpsertSystemControlPlane(item)
		return
	}

	runtime := strings.ToLower(string(status.Status))
	item.RuntimeState = runtime
	item.State = runtime
	item.ContainerID = container.ID
	item.LastTransitionAt = now

	switch runtime {
	case "running":
		item.ObservedGeneration = item.Generation
		item.FailureCount = 0
	case "created":
		if err := pm.startLocalMicroservice(item.ControllerUUID); err != nil {
			if errors.Is(err, errVolumeInUse) {
				pm.noteControlPlaneVolumeHold(item)
				return
			}
			if nr, ok := engine.IsNonRestartableContainerError(err); ok {
				pm.logger.Warnf(
					"control plane reconcile created start failed with non-restartable terminal state uuid=%s containerID=%s reason=%s exitCode=%d criMessage=%q decision=recreate",
					item.ControllerUUID,
					container.ID,
					nr.Reason,
					nr.ExitCode,
					nr.Message,
				)
				if recErr := pm.recreateControlPlaneDeployment(item, false, now); recErr == nil || errors.Is(recErr, errVolumeInUse) {
					return
				}
			}
			pm.bumpControlPlaneFailure(item, err, runtime)
		} else {
			item.RuntimeState = "running"
			item.State = item.RuntimeState
			item.ObservedGeneration = item.Generation
			item.FailureCount = 0
		}
	case "exiting":
		if force, reason, exitCode := shouldForceRecreateFromStatus(status); force {
			pm.logger.Warnf(
				"control plane reconcile exiting with non-restartable terminal state uuid=%s containerID=%s reason=%s exitCode=%d decision=recreate",
				item.ControllerUUID,
				container.ID,
				reason,
				exitCode,
			)
			if recErr := pm.recreateControlPlaneDeployment(item, false, now); recErr == nil || errors.Is(recErr, errVolumeInUse) {
				return
			}
		}
		if err := pm.startLocalMicroservice(item.ControllerUUID); err != nil {
			if errors.Is(err, errVolumeInUse) {
				pm.noteControlPlaneVolumeHold(item)
				return
			}
			if nr, ok := engine.IsNonRestartableContainerError(err); ok {
				pm.logger.Warnf(
					"control plane reconcile exiting start failed with non-restartable terminal state uuid=%s containerID=%s reason=%s exitCode=%d criMessage=%q decision=recreate",
					item.ControllerUUID,
					container.ID,
					nr.Reason,
					nr.ExitCode,
					nr.Message,
				)
				if recErr := pm.recreateControlPlaneDeployment(item, false, now); recErr == nil || errors.Is(recErr, errVolumeInUse) {
					return
				}
			}
			pm.bumpControlPlaneFailure(item, err, runtime)
		} else {
			item.RuntimeState = "running"
			item.State = item.RuntimeState
			item.ObservedGeneration = item.Generation
			item.FailureCount = 0
		}
		_ = store.GetInstance().UpsertSystemControlPlane(item)
		pm.mergeControlPlaneContainerStats(item, container, status)
		pm.syncControlPlaneProcessManagerStatus(item, container, status)
		return
	case "failed", "unknown":
		pm.bumpControlPlaneFailure(item, fmt.Errorf("runtime state=%s", runtime), runtime)
	default:
		if status.ErrorMessage != nil {
			item.LastError = strings.TrimSpace(*status.ErrorMessage)
		}
	}

	_ = store.GetInstance().UpsertSystemControlPlane(item)
	pm.mergeControlPlaneContainerStats(item, container, status)
	pm.syncControlPlaneProcessManagerStatus(item, container, status)
}

func (pm *ProcessManager) mergeControlPlaneContainerStats(
	item *models.ControlPlaneDeployment,
	container *engine.Container,
	status *models.MicroserviceStatus,
) {
	if pm == nil || item == nil || container == nil || status == nil {
		return
	}
	if !item.ControllerRegistered {
		return
	}
	if status.Status != models.MicroserviceStateRunning {
		return
	}
	if pm.engine == nil {
		return
	}
	if stats, err := pm.engine.GetContainerStats(container.ID); err == nil {
		status.CPUUsage = stats.CPUUsage
		status.MemoryUsage = stats.MemoryUsage
	}
}

func (pm *ProcessManager) syncControlPlaneProcessManagerStatus(
	item *models.ControlPlaneDeployment,
	container *engine.Container,
	status *models.MicroserviceStatus,
) {
	if item == nil {
		return
	}
	uuid := strings.TrimSpace(item.ControllerUUID)
	if uuid == "" {
		return
	}
	runtimeStatus := status
	var synced *models.MicroserviceStatus
	statusreporter.GetInstance().UpdateProcessManagerStatus(func(pmStatus *models.ProcessManagerStatus) {
		next := runtimeStatus
		if next == nil {
			next = models.NewMicroserviceStatusWithState(controlPlaneRuntimeStateToMicroserviceState(item.RuntimeState))
			if container != nil {
				next.ContainerID = container.ID
			}
		}
		syncMicroserviceStatusToReporter(pmStatus, uuid, next)
		synced = next
	})
	maybeResetRestartBackoff(uuid, synced)
}

func controlPlaneRuntimeStateToMicroserviceState(runtimeState string) models.MicroserviceState {
	switch strings.ToLower(strings.TrimSpace(runtimeState)) {
	case "running":
		return models.MicroserviceStateRunning
	case "starting", "stopping", "deleting":
		return models.MicroserviceStateUpdating
	case "stopped", "created":
		return models.MicroserviceStateCreated
	case "failed", "stuck_in_restart":
		return models.MicroserviceStateFailed
	case "exiting":
		return models.MicroserviceStateExiting
	default:
		return models.MicroserviceStateUnknown
	}
}

func controlPlaneLaunchInFlight(item *models.ControlPlaneDeployment, now int64) bool {
	if item == nil {
		return false
	}
	if strings.ToLower(strings.TrimSpace(item.RuntimeState)) != "starting" {
		return false
	}
	if item.Generation <= item.ObservedGeneration {
		return false
	}
	startedAt := item.LastStartAttemptAt
	if startedAt <= 0 {
		startedAt = item.LastTransitionAt
	}
	if startedAt > 0 && now-startedAt > int64(localLaunchInFlightStaleTimeout.Seconds()) {
		return false
	}
	return true
}

func (pm *ProcessManager) bumpControlPlaneFailure(item *models.ControlPlaneDeployment, cause error, runtime string) {
	item.FailureCount++
	item.RestartCount++
	if cause != nil {
		item.LastError = cause.Error()
	}
	if item.FailureCount >= localReconcileMaxFailures {
		item.RuntimeState = "stuck_in_restart"
		item.State = item.RuntimeState
		return
	}
	item.RuntimeState = runtime
	item.State = item.RuntimeState
}

func (pm *ProcessManager) noteControlPlaneVolumeHold(item *models.ControlPlaneDeployment) {
	if item == nil {
		return
	}
	pm.noteVolumeInUse(item.ControllerUUID)
	item.LastError = volumeInUseStatusText
	_ = store.GetInstance().UpsertSystemControlPlane(item)
}

func (pm *ProcessManager) launchControlPlaneWithHook(item *models.ControlPlaneDeployment, now int64) {
	if pm.launchControlPlaneFn != nil {
		pm.launchControlPlaneFn(item, now)
		return
	}
	pm.launchControlPlane(item, now)
}

func (pm *ProcessManager) launchControlPlane(item *models.ControlPlaneDeployment, now int64) {
	pm.launchControlPlaneWithProgress(item, now, nil)
}

func (pm *ProcessManager) launchControlPlaneWithProgress(item *models.ControlPlaneDeployment, now int64, progress LocalDeployProgressCallback) {
	doc, ms, registry, err := pm.buildControlPlaneLaunchSpec(item)
	if err != nil {
		item.RuntimeState = "failed"
		item.State = item.RuntimeState
		pm.bumpControlPlaneFailure(item, err, item.RuntimeState)
		item.LastTransitionAt = now
		_ = store.GetInstance().UpsertSystemControlPlane(item)
		return
	}
	if pm.deferVolumeCreate(ms, nil) {
		item.LastError = volumeInUseStatusText
		item.LastTransitionAt = now
		_ = store.GetInstance().UpsertSystemControlPlane(item)
		return
	}

	item.RuntimeState = "starting"
	item.State = item.RuntimeState
	item.LastStartAttemptAt = now
	item.LastTransitionAt = now
	item.Image = doc.ManifestControllerImage()
	_ = store.GetInstance().UpsertSystemControlPlane(item)

	hostIP := network.GetInstance().GetCurrentIPAddress()
	containerID, err := pm.LaunchLocalMicroserviceWithProgress(ms, registry, hostIP, progress)
	if err != nil {
		if errors.Is(err, errVolumeInUse) {
			pm.noteControlPlaneVolumeHold(item)
			return
		}
		item.RuntimeState = "failed"
		item.State = item.RuntimeState
		pm.bumpControlPlaneFailure(item, err, item.RuntimeState)
		item.LastTransitionAt = now
		_ = store.GetInstance().UpsertSystemControlPlane(item)
		return
	}

	item.ContainerID = containerID
	item.Image = ms.ImageName
	item.RuntimeState = "running"
	item.State = item.RuntimeState
	item.ObservedGeneration = item.Generation
	item.FailureCount = 0
	item.LastTransitionAt = now
	_ = store.GetInstance().UpsertSystemControlPlane(item)
	pm.syncControlPlaneDNS(item, true)
}

func (pm *ProcessManager) recreateControlPlaneDeployment(item *models.ControlPlaneDeployment, pullImage bool, now int64) error {
	return pm.recreateControlPlaneDeploymentWithProgress(item, pullImage, now, nil)
}

func (pm *ProcessManager) recreateControlPlaneDeploymentWithProgress(item *models.ControlPlaneDeployment, pullImage bool, now int64, progress LocalDeployProgressCallback) error {
	if pm.recreateControlPlaneFn != nil {
		return pm.recreateControlPlaneFn(item, pullImage, now)
	}
	if pm.containerManager == nil {
		err := errors.New("process manager is not initialized")
		pm.bumpControlPlaneFailure(item, err, "failed")
		return err
	}
	_, ms, registry, err := pm.buildControlPlaneLaunchSpec(item)
	if err != nil {
		pm.bumpControlPlaneFailure(item, err, "failed")
		return err
	}
	if existing, lookupErr := pm.containerForControlPlane(item.ControllerUUID, item.ContainerID); lookupErr == nil && existing == nil && pm.volumeHeld(ms) {
		pm.noteControlPlaneVolumeHold(item)
		return errVolumeInUse
	}
	if pullImage {
		pm.pullControlPlaneImage(ms, registry)
	}
	if container, contErr := pm.containerForControlPlane(item.ControllerUUID, item.ContainerID); contErr == nil && container != nil {
		_ = pm.removeLocalContainerByID(container.ID)
	}
	containerID, err := pm.LaunchLocalMicroserviceWithProgress(ms, registry, network.GetInstance().GetCurrentIPAddress(), progress)
	if err != nil {
		if errors.Is(err, errVolumeInUse) {
			pm.noteControlPlaneVolumeHold(item)
			return err
		}
		pm.bumpControlPlaneFailure(item, err, "failed")
		return err
	}
	item.ContainerID = containerID
	item.Image = ms.ImageName
	item.RuntimeState = "running"
	item.State = item.RuntimeState
	item.ObservedGeneration = item.Generation
	item.FailureCount = 0
	item.LastTransitionAt = now
	if err := store.GetInstance().UpsertSystemControlPlane(item); err != nil {
		return err
	}
	pm.syncControlPlaneDNS(item, true)
	return nil
}

func (pm *ProcessManager) buildControlPlaneLaunchSpec(item *models.ControlPlaneDeployment) (*models.ControlPlaneManifest, *models.Microservice, *models.Registry, error) {
	if item == nil {
		return nil, nil, nil, errors.New("control plane deployment is nil")
	}
	doc, err := models.ParseControlPlaneManifest(item.ManifestYAML)
	if err != nil {
		return nil, nil, nil, err
	}
	image := doc.ManifestControllerImage()
	ms, err := controlplane.BuildMicroserviceFromControlPlane(doc, item.ControllerUUID, image)
	if err != nil {
		return nil, nil, nil, err
	}
	registry := models.NewRegistry(2, "from_cache", true, "", "", "")
	if regID, ok := doc.ControllerRegistryID(); ok {
		if reg, regErr := store.GetInstance().GetLocalRegistry(regID); regErr == nil && reg != nil {
			registry = reg
			ms.RegistryID = reg.ID
		}
	}
	return doc, ms, registry, nil
}

func (pm *ProcessManager) pullControlPlaneImage(ms *models.Microservice, registry *models.Registry) {
	if pm == nil || pm.engine == nil || ms == nil || registry == nil {
		return
	}
	if err := models.RequireOCIForImagePull(registry); err != nil {
		pm.logger.Warnf("control plane image pull rejected: %v", err)
		return
	}
	pullRef, _, fromCache := imageref.ResolveForRegistry(ms.ImageName, registry.URL)
	opts := &engine.PullImageOptions{Platform: msPlatform(ms)}
	if !fromCache {
		if err := pm.engine.PullImage(pullRef, registry, opts); err != nil {
			pm.logger.Warnf("control plane recreate pull failed for %s, continuing with cache: %v", pullRef, err)
			return
		}
	}
	ms.ImageName = pullRef
}
