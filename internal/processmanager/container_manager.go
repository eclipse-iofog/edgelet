package processmanager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/network"
	"github.com/eclipse-iofog/edgelet/internal/runtimeops"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/eclipse-iofog/edgelet/internal/volumemount"
	"github.com/eclipse-iofog/edgelet/internal/workloadmeta"
	"github.com/eclipse-iofog/edgelet/pkg/engine"
	"github.com/eclipse-iofog/edgelet/pkg/imageref"
)

const (
	ContainerManagerModuleName = "Container Manager"
)

// ContainerManager manages container operations via a ContainerEngine.
type ContainerManager struct {
	engine               engine.ContainerEngine
	microserviceManager  MicroserviceManagerInterface
	engineName           string
	logger               *logging.ModuleLogger
	catalogDiskDirectory string
}

// NewContainerManager creates a new ContainerManager
func NewContainerManager(eng engine.ContainerEngine, microserviceManager MicroserviceManagerInterface, engineName string) *ContainerManager {
	return &ContainerManager{
		engine:              eng,
		microserviceManager: microserviceManager,
		engineName:          engineName,
		logger:              logging.NewModuleLogger(ContainerManagerModuleName),
	}
}

// GetContainerForMicroservice returns the container for a microservice, using DB-first
// lookup when available (iofog engine) with label-based fallback.
func (cm *ContainerManager) GetContainerForMicroservice(microserviceUUID string) (*engine.Container, error) {
	if cs, err := store.GetInstance().GetRuntimeContainerRef(microserviceUUID, store.RuntimeScopeController); err == nil && cs != nil && cs.WorkloadID != "" {
		if c, err := cm.engine.GetContainerByID(cs.WorkloadID); err == nil && c != nil {
			return c, nil
		}
		cm.logger.Warnf(
			"runtime drift cleanup: stale runtime_container_refs (controller) msUUID=%s containerID=%s decision=cleanup-stale-db",
			microserviceUUID,
			cs.WorkloadID,
		)
		_ = store.GetInstance().DeleteRuntimeContainerRef(microserviceUUID, store.RuntimeScopeController)
	}
	if cs, err := store.GetInstance().GetRuntimeContainerRef(microserviceUUID, store.RuntimeScopeLocal); err == nil && cs != nil && cs.WorkloadID != "" {
		if c, err := cm.engine.GetContainerByID(cs.WorkloadID); err == nil && c != nil {
			return c, nil
		}
		cm.logger.Warnf(
			"runtime drift cleanup: stale runtime_container_refs (local) msUUID=%s containerID=%s decision=cleanup-stale-db",
			microserviceUUID,
			cs.WorkloadID,
		)
		_ = store.GetInstance().DeleteRuntimeContainerRef(microserviceUUID, store.RuntimeScopeLocal)
	}
	c, err := cm.engine.GetContainer(microserviceUUID)
	if err != nil {
		return nil, err
	}
	if c != nil {
		if sandboxID, _ := cm.engine.GetContainerSandboxID(c.ID); sandboxID != "" {
			_ = store.GetInstance().UpsertRuntimeContainerRef(microserviceUUID, store.RuntimeScopeController, c.ID, sandboxID)
		}
	}
	return c, nil
}

// AddContainer creates and starts a container for a microservice.
// It holds IsUpdating=true for the duration so the reconciliation loop treats this
// microservice as "in-flight" and does not enqueue a second ADD task.
func (cm *ContainerManager) AddContainer(ctx context.Context, ms *models.Microservice) error {
	ms.SetIsUpdating(true)
	defer ms.SetIsUpdating(false)

	container, err := cm.GetContainerForMicroservice(ms.MicroserviceUUID)
	if err != nil {
		return err
	}

	if container == nil {
		return cm.createContainer(ctx, ms)
	}

	// Container already exists (created by a concurrent task) — nothing to do.
	return nil
}

// UpdateContainer updates a container for a microservice
func (cm *ContainerManager) UpdateContainer(ctx context.Context, ms *models.Microservice, withCleanup bool) error {
	ms.SetIsUpdating(true)
	defer ms.SetIsUpdating(false)

	// Step 1: Pull new image while old container is still running
	// This keeps the service available during the slow image pull operation
	registry, err := resolveRegistryForMicroservice(cm.microserviceManager, ms)
	if err != nil {
		return err
	}
	pullRef, lookupRefs, fromCache := imageref.ResolveForRegistry(ms.ImageName, registry.URL)
	pullSucceeded := false

	if !fromCache {
		cm.emitFromCM(ctx, runtimeops.RuntimeEvent{
			Event:   runtimeops.EventContainerUpdatePhase,
			MsUUID:  ms.MicroserviceUUID,
			Image:   pullRef,
			Phase:   "pull",
			Source:  runtimeops.SourceTask,
			Message: "update pull phase",
		})
		opts := &engine.PullImageOptions{
			Platform: cm.platformForPull(ms),
			ProgressCallback: func(pct float32) {
				statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
					s.SetMicroservicesStatePercentage(ms.MicroserviceUUID, pct)
				})
			},
		}
		pullStart := time.Now()
		if err := cm.engine.PullImage(pullRef, registry, opts); err != nil {
			cm.emitFromCM(ctx, runtimeops.RuntimeEvent{
				Event:      runtimeops.EventContainerPullCompleted,
				Level:      runtimeops.LevelWarn,
				MsUUID:     ms.MicroserviceUUID,
				Image:      pullRef,
				Phase:      "pull",
				Result:     runtimeops.ResultFailed,
				ReasonCode: runtimeops.ReasonPullCacheFallback,
				DurationMs: time.Since(pullStart).Milliseconds(),
				Error:      err.Error(),
				Message:    "update pull failed, using local cache",
			})
			cm.logger.Warnf("Unable to pull \"%s\" from registry. Trying local cache: %v", pullRef, err)
		} else {
			pullSucceeded = true
			cm.emitFromCM(ctx, runtimeops.RuntimeEvent{
				Event:      runtimeops.EventContainerPullCompleted,
				MsUUID:     ms.MicroserviceUUID,
				Image:      pullRef,
				Phase:      "pull",
				Result:     runtimeops.ResultOK,
				DurationMs: time.Since(pullStart).Milliseconds(),
				Message:    "update pull completed",
			})
			statusreporter.GetInstance().UpdateProcessManagerStatus(func(status *models.ProcessManagerStatus) {
				status.SetMicroservicesStatePercentage(ms.MicroserviceUUID, 100.0)
			})
		}
	}

	// Verify image exists (either pulled or in cache) and pick the runtime reference
	// that will be used for container creation.
	matchedRef, exists, err := cm.findFirstLocalImageRef(lookupRefs, registry)
	if err != nil {
		return err
	}
	if !exists {
		ms.SetIsUpdating(false)
		return fmt.Errorf("image not found in local cache for refs: %v", lookupRefs)
	}
	effectiveRunRef := matchedRef
	if pullSucceeded {
		effectiveRunRef = pullRef
	}

	// Step 2: Now stop and remove old container (releases ports)
	// Downtime starts here, but it's brief compared to pull time
	// removeImage=withCleanup: image deleted on clean update
	cm.emitFromCM(ctx, runtimeops.RuntimeEvent{
		Event:   runtimeops.EventContainerUpdatePhase,
		MsUUID:  ms.MicroserviceUUID,
		Image:   effectiveRunRef,
		Phase:   "remove",
		Source:  runtimeops.SourceTask,
		Message: "update remove phase",
	})
	if err := cm.RemoveContainerByMicroserviceUUID(ctx, ms.MicroserviceUUID, withCleanup, withCleanup); err != nil {
		cm.logger.Warnf("Error removing old container: %v", err)
		// Continue anyway
	}

	// Step 3: Create and start new container (can use same ports now)
	ms.ImageName = effectiveRunRef
	cm.emitFromCM(ctx, runtimeops.RuntimeEvent{
		Event:   runtimeops.EventContainerUpdatePhase,
		MsUUID:  ms.MicroserviceUUID,
		Image:   effectiveRunRef,
		Phase:   "create",
		Source:  runtimeops.SourceTask,
		Message: "update create phase",
	})
	return cm.createContainer(ctx, ms)
}

// RemoveContainerByMicroserviceUUID removes a container by microservice UUID.
// withCleanup is passed to engine.RemoveContainer. Persistent VOLUME data is
// bind-mounted and is not deleted when the container is removed.
// removeImage controls whether the container image is also removed after container deletion —
// set true for normal lifecycle deletions,
// false for the deprovision path.
func (cm *ContainerManager) RemoveContainerByMicroserviceUUID(ctx context.Context, microserviceUUID string, withCleanup bool, removeImage bool) error {
	container, err := cm.GetContainerForMicroservice(microserviceUUID)
	if err != nil {
		return err
	}
	// Fallback for watchdog/unknown-container flows where the incoming identifier
	// can be a concrete container ID (or other non-iofog name) that doesn't resolve
	// through microservice UUID lookup.
	if container == nil {
		container, err = cm.engine.GetContainerByID(microserviceUUID)
		if err != nil {
			return err
		}
	}

	if container == nil {
		cm.emitFromCM(ctx, runtimeops.RuntimeEvent{
			Event:   runtimeops.EventContainerRemoved,
			MsUUID:  microserviceUUID,
			Result:  runtimeops.ResultSkipped,
			Source:  runtimeops.SourceTask,
			Message: "container already removed",
		})
		statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
			s.SetMicroservicesState(microserviceUUID, models.MicroserviceStateDeleted)
			s.SetMicroservicesStatusErrorMessage(microserviceUUID, "")
		})
		_ = store.GetInstance().DeleteRuntimeContainerRef(microserviceUUID, store.RuntimeScopeController)
		_ = store.GetInstance().DeleteRuntimeContainerRef(microserviceUUID, store.RuntimeScopeLocal)
	} else {
		imageRef := container.Image
		statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
			s.SetMicroservicesState(microserviceUUID, models.MicroserviceStateDeleting)
		})
		if err := cm.removeRuntimeContainer(ctx, microserviceUUID, container.ID, imageRef, runtimeops.SourceTask, withCleanup, removeImage); err != nil {
			return err
		}
		statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
			s.SetMicroservicesState(microserviceUUID, models.MicroserviceStateDeleted)
			s.SetMicroservicesStatusErrorMessage(microserviceUUID, "")
		})
		_ = store.GetInstance().DeleteRuntimeContainerRef(microserviceUUID, store.RuntimeScopeController)
		_ = store.GetInstance().DeleteRuntimeContainerRef(microserviceUUID, store.RuntimeScopeLocal)
	}

	volumemount.GetInstance().CleanupMicroserviceVolumes(microserviceUUID)
	cm.releaseCatalog(microserviceUUID)
	return nil
}

// RemoveContainerByID removes a container by concrete engine-assigned container ID.
// This is primarily used by watchdog unknown-container cleanup where there is no
// guaranteed iofog microservice UUID mapping.
func (cm *ContainerManager) RemoveContainerByID(ctx context.Context, containerID string, withCleanup bool, removeImage bool) error {
	container, err := cm.engine.GetContainerByID(containerID)
	if err != nil {
		return err
	}
	if container == nil {
		cm.emitFromCM(ctx, runtimeops.RuntimeEvent{
			Event:       runtimeops.EventContainerRemoved,
			ContainerID: containerID,
			Result:      runtimeops.ResultSkipped,
			Source:      runtimeops.SourceWatchdog,
			Message:     "container already removed",
		})
		return nil
	}

	imageRef := container.Image
	msUUID := ""
	if workloadmeta.IsManagedByIofog(container.Labels) {
		msUUID = workloadmeta.MicroserviceUIDFromLabels(container.Labels)
	}

	if err := cm.removeRuntimeContainer(ctx, msUUID, container.ID, imageRef, runtimeops.SourceWatchdog, withCleanup, removeImage); err != nil {
		return err
	}

	if msUUID != "" {
		statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
			s.SetMicroservicesState(msUUID, models.MicroserviceStateDeleted)
			s.SetMicroservicesStatusErrorMessage(msUUID, "")
		})
		_ = store.GetInstance().DeleteRuntimeContainerRef(msUUID, store.RuntimeScopeController)
		_ = store.GetInstance().DeleteRuntimeContainerRef(msUUID, store.RuntimeScopeLocal)
		_ = store.GetInstance().DeleteLocalWorkload(msUUID)
		volumemount.GetInstance().CleanupMicroserviceVolumes(msUUID)
		cm.releaseCatalog(msUUID)
	}

	return nil
}

// RemoveContainerRuntimeForEngineSwitch removes a workload container without deleting deploy spec rows.
func (cm *ContainerManager) RemoveContainerRuntimeForEngineSwitch(ctx context.Context, containerID string) error {
	container, err := cm.engine.GetContainerByID(containerID)
	if err != nil {
		return err
	}
	if container == nil {
		return nil
	}
	msUUID := ""
	if workloadmeta.IsManagedByIofog(container.Labels) {
		msUUID = workloadmeta.MicroserviceUIDFromLabels(container.Labels)
	}
	return cm.removeRuntimeContainer(ctx, msUUID, container.ID, container.Image, runtimeops.SourceTask, false, false)
}

// StopContainerByMicroserviceUUID stops a container by microservice UUID
func (cm *ContainerManager) StopContainerByMicroserviceUUID(ctx context.Context, microserviceUUID string) error {
	container, err := cm.GetContainerForMicroservice(microserviceUUID)
	if err != nil {
		return err
	}
	if container == nil {
		cm.emitFromCM(ctx, runtimeops.RuntimeEvent{
			Event:   runtimeops.EventContainerStopped,
			MsUUID:  microserviceUUID,
			Result:  runtimeops.ResultSkipped,
			Source:  runtimeops.SourceTask,
			Message: "stop skipped, container not found",
		})
		return nil
	}

	cm.emitFromCM(ctx, runtimeops.RuntimeEvent{
		Event:       runtimeops.EventContainerStopping,
		MsUUID:      microserviceUUID,
		ContainerID: container.ID,
		Image:       container.Image,
		Source:      runtimeops.SourceTask,
		Message:     "stopping container",
	})
	stopStart := time.Now()
	stopErr := cm.engine.StopContainer(container.ID)
	stopEvent := runtimeops.RuntimeEvent{
		Event:       runtimeops.EventContainerStopped,
		MsUUID:      microserviceUUID,
		ContainerID: container.ID,
		Image:       container.Image,
		Source:      runtimeops.SourceTask,
		DurationMs:  time.Since(stopStart).Milliseconds(),
		Message:     "container stopped",
	}
	if stopErr != nil {
		stopEvent.Level = runtimeops.LevelWarn
		stopEvent.Result = runtimeops.ResultFailed
		stopEvent.ReasonCode = runtimeops.ReasonStopFailed
		stopEvent.Error = stopErr.Error()
		stopEvent.Message = "container stop failed"
		cm.emitFromCM(ctx, stopEvent)
		return stopErr
	}
	stopEvent.Result = runtimeops.ResultOK
	cm.emitFromCM(ctx, stopEvent)
	return nil
}

// StartContainerByMicroserviceUUID starts a container by microservice UUID.
func (cm *ContainerManager) StartContainerByMicroserviceUUID(ctx context.Context, microserviceUUID string) error {
	container, err := cm.GetContainerForMicroservice(microserviceUUID)
	if err != nil {
		return err
	}
	if container == nil {
		cm.emitFromCM(ctx, runtimeops.RuntimeEvent{
			Event:   runtimeops.EventContainerStarted,
			MsUUID:  microserviceUUID,
			Result:  runtimeops.ResultSkipped,
			Source:  runtimeops.SourceTask,
			Message: "start skipped, container not found",
		})
		return nil
	}

	cm.emitFromCM(ctx, runtimeops.RuntimeEvent{
		Event:       runtimeops.EventContainerStarting,
		MsUUID:      microserviceUUID,
		ContainerID: container.ID,
		Image:       container.Image,
		Source:      runtimeops.SourceTask,
		Message:     "starting container",
	})
	startAt := time.Now()
	startErr := cm.engine.StartContainer(container.ID)
	if startErr == nil {
		cm.emitFromCM(ctx, runtimeops.RuntimeEvent{
			Event:       runtimeops.EventContainerStarted,
			MsUUID:      microserviceUUID,
			ContainerID: container.ID,
			Image:       container.Image,
			Source:      runtimeops.SourceTask,
			Result:      runtimeops.ResultOK,
			DurationMs:  time.Since(startAt).Milliseconds(),
			Message:     "container started",
		})
		return nil
	}

	var nr *engine.NonRestartableContainerError
	if errors.As(startErr, &nr) {
		cm.emitFromCM(ctx, runtimeops.RuntimeEvent{
			Event:       runtimeops.EventContainerStarted,
			Level:       runtimeops.LevelWarn,
			MsUUID:      microserviceUUID,
			ContainerID: nr.ContainerID,
			Image:       container.Image,
			Source:      runtimeops.SourceTask,
			Result:      runtimeops.ResultFailed,
			ReasonCode:  runtimeops.ReasonNonRestartableExit,
			DurationMs:  time.Since(startAt).Milliseconds(),
			Error:       nr.Message,
			Message:     "non-restartable container start failure",
			Fields: map[string]any{
				"exitCode": nr.ExitCode,
				"reason":   nr.Reason,
			},
		})
	} else {
		cm.emitFromCM(ctx, runtimeops.RuntimeEvent{
			Event:       runtimeops.EventContainerStarted,
			Level:       runtimeops.LevelWarn,
			MsUUID:      microserviceUUID,
			ContainerID: container.ID,
			Image:       container.Image,
			Source:      runtimeops.SourceTask,
			Result:      runtimeops.ResultFailed,
			ReasonCode:  runtimeops.ReasonStartFailed,
			DurationMs:  time.Since(startAt).Milliseconds(),
			Error:       startErr.Error(),
			Message:     "container start failed",
		})
	}
	return startErr
}

// KillContainerByMicroserviceUUID forcefully stops a container by microservice UUID.
func (cm *ContainerManager) KillContainerByMicroserviceUUID(ctx context.Context, microserviceUUID string) error {
	container, err := cm.GetContainerForMicroservice(microserviceUUID)
	if err != nil {
		return err
	}
	if container == nil {
		cm.emitFromCM(ctx, runtimeops.RuntimeEvent{
			Event:   runtimeops.EventContainerStopped,
			MsUUID:  microserviceUUID,
			Result:  runtimeops.ResultSkipped,
			Source:  runtimeops.SourceTask,
			Message: "kill skipped, container not found",
			Fields:  map[string]any{"force": true},
		})
		return nil
	}

	cm.emitFromCM(ctx, runtimeops.RuntimeEvent{
		Event:       runtimeops.EventContainerStopping,
		MsUUID:      microserviceUUID,
		ContainerID: container.ID,
		Image:       container.Image,
		Source:      runtimeops.SourceTask,
		Message:     "force stopping container",
		Fields:      map[string]any{"force": true},
	})
	killStart := time.Now()
	killErr := cm.engine.KillContainer(container.ID)
	killEvent := runtimeops.RuntimeEvent{
		Event:       runtimeops.EventContainerStopped,
		MsUUID:      microserviceUUID,
		ContainerID: container.ID,
		Image:       container.Image,
		Source:      runtimeops.SourceTask,
		DurationMs:  time.Since(killStart).Milliseconds(),
		Message:     "container force stopped",
		Fields:      map[string]any{"force": true},
	}
	if killErr != nil {
		killEvent.Level = runtimeops.LevelWarn
		killEvent.Result = runtimeops.ResultFailed
		killEvent.ReasonCode = runtimeops.ReasonStopFailed
		killEvent.Error = killErr.Error()
		killEvent.Message = "container force stop failed"
		cm.emitFromCM(ctx, killEvent)
		return killErr
	}
	killEvent.Result = runtimeops.ResultOK
	cm.emitFromCM(ctx, killEvent)
	return nil
}

// RecreateOptions controls remove+create lifecycle recreation.
type RecreateOptions struct {
	PullImage   bool
	WithCleanup bool
	RemoveImage bool
}

// RecreateContainer removes an existing workload container when present, then
// creates and starts a new one from the microservice spec.
func (cm *ContainerManager) RecreateContainer(ctx context.Context, ms *models.Microservice, opts RecreateOptions) (string, error) {
	if ms == nil {
		return "", errors.New("microservice is nil")
	}

	cm.emitFromCM(ctx, runtimeops.RuntimeEvent{
		Event:   runtimeops.EventContainerUpdatePhase,
		MsUUID:  ms.MicroserviceUUID,
		Image:   ms.ImageName,
		Phase:   "recreate",
		Source:  runtimeops.SourceTask,
		Message: "recreate phase",
	})

	container, err := cm.GetContainerForMicroservice(ms.MicroserviceUUID)
	if err != nil {
		return "", err
	}
	if container != nil {
		if err := cm.RemoveContainerByMicroserviceUUID(ctx, ms.MicroserviceUUID, opts.WithCleanup, opts.RemoveImage); err != nil {
			return "", err
		}
	}

	if err := cm.createContainerWithPull(ctx, ms, opts.PullImage); err != nil {
		return "", err
	}
	if strings.TrimSpace(ms.ContainerID) == "" {
		return "", fmt.Errorf("recreate completed without container id for %s", ms.MicroserviceUUID)
	}
	return ms.ContainerID, nil
}

// createContainer creates a container for a microservice
func (cm *ContainerManager) createContainer(ctx context.Context, ms *models.Microservice) error {
	return cm.createContainerWithPull(ctx, ms, true)
}

// createContainerWithPull creates a container, optionally pulling the image first
func (cm *ContainerManager) createContainerWithPull(ctx context.Context, ms *models.Microservice, pullImage bool) error {
	if err := cm.applyCatalogStartGate(ms); err != nil {
		return err
	}
	if cm.volumeHeld(ms) {
		return errVolumeInUse
	}
	statusreporter.GetInstance().UpdateProcessManagerStatus(func(status *models.ProcessManagerStatus) {
		status.SetMicroservicesState(ms.MicroserviceUUID, models.MicroserviceStatePulling)
	})

	registry, err := resolveRegistryForMicroservice(cm.microserviceManager, ms)
	if err != nil {
		return err
	}
	pullRef, lookupRefs, fromCache := imageref.ResolveForRegistry(ms.ImageName, registry.URL)
	pullSucceeded := false

	if !fromCache && pullImage {
		cm.emitFromCM(ctx, runtimeops.RuntimeEvent{
			Event:   runtimeops.EventContainerPulling,
			MsUUID:  ms.MicroserviceUUID,
			Image:   pullRef,
			Phase:   "pull",
			Source:  runtimeops.SourceTask,
			Message: "pulling image",
		})
		opts := &engine.PullImageOptions{
			Platform: cm.platformForPull(ms),
			ProgressCallback: func(pct float32) {
				statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
					s.SetMicroservicesStatePercentage(ms.MicroserviceUUID, pct)
				})
			},
		}
		pullStart := time.Now()
		if err := cm.engine.PullImage(pullRef, registry, opts); err != nil {
			cm.emitFromCM(ctx, runtimeops.RuntimeEvent{
				Event:      runtimeops.EventContainerPullCompleted,
				Level:      runtimeops.LevelWarn,
				MsUUID:     ms.MicroserviceUUID,
				Image:      pullRef,
				Phase:      "pull",
				Result:     runtimeops.ResultFailed,
				ReasonCode: runtimeops.ReasonPullCacheFallback,
				DurationMs: time.Since(pullStart).Milliseconds(),
				Error:      err.Error(),
				Message:    "pull failed, trying local cache",
			})
			cm.logger.Warnf("Unable to pull \"%s\" from registry. Trying local cache: %v", pullRef, err)
			return cm.createContainerWithPull(ctx, ms, false)
		}
		pullSucceeded = true
		cm.emitFromCM(ctx, runtimeops.RuntimeEvent{
			Event:      runtimeops.EventContainerPullCompleted,
			MsUUID:     ms.MicroserviceUUID,
			Image:      pullRef,
			Phase:      "pull",
			Result:     runtimeops.ResultOK,
			DurationMs: time.Since(pullStart).Milliseconds(),
			Message:    "pull completed",
		})
		statusreporter.GetInstance().UpdateProcessManagerStatus(func(status *models.ProcessManagerStatus) {
			status.SetMicroservicesStatePercentage(ms.MicroserviceUUID, 100.0)
		})
	} else {
		cm.emitFromCM(ctx, runtimeops.RuntimeEvent{
			Event:   runtimeops.EventContainerPullCompleted,
			MsUUID:  ms.MicroserviceUUID,
			Image:   pullRef,
			Phase:   "pull",
			Result:  runtimeops.ResultSkipped,
			Source:  runtimeops.SourceTask,
			Message: "pull skipped, using local cache",
		})
	}

	// Verify image exists in local cache and pick the runtime image reference.
	matchedRef, exists, err := cm.findFirstLocalImageRef(lookupRefs, registry)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("image not found in local cache for refs: %v", lookupRefs)
	}
	effectiveRunRef := matchedRef
	if pullSucceeded {
		effectiveRunRef = pullRef
	}
	ms.ImageName = effectiveRunRef

	cm.emitFromCM(ctx, runtimeops.RuntimeEvent{
		Event:   runtimeops.EventContainerCreating,
		MsUUID:  ms.MicroserviceUUID,
		Image:   effectiveRunRef,
		Phase:   "create",
		Source:  runtimeops.SourceTask,
		Message: "creating container",
	})
	statusreporter.GetInstance().UpdateProcessManagerStatus(func(status *models.ProcessManagerStatus) {
		status.SetMicroservicesState(ms.MicroserviceUUID, models.MicroserviceStateStarting)
	})

	cfg := config.GetInstance()
	// Get host IP from network interface manager
	// Used for iofog and service.local extra hosts so containers can reach the host/agent.
	networkManager := network.GetInstance()
	hostIP := networkManager.GetCurrentIPAddress()
	if hostIP == "" {
		// Retry
		for tries := 0; tries < 5 && hostIP == ""; tries++ {
			time.Sleep(500 * time.Millisecond)
			hostIP = networkManager.GetCurrentIPAddress()
		}
		if hostIP == "" {
			hostIP = "127.0.0.1"
			cm.logger.Infof("host IP unavailable, using fallback %q", hostIP)
		}
	}
	_ = cfg

	createStart := time.Now()
	containerID, err := cm.engine.CreateContainer(ms, hostIP)
	if err != nil {
		if errors.Is(err, engine.ErrReconcilePaused) {
			return err
		}
		cm.emitFromCM(ctx, runtimeops.RuntimeEvent{
			Event:      runtimeops.EventContainerCreated,
			Level:      runtimeops.LevelError,
			MsUUID:     ms.MicroserviceUUID,
			Image:      effectiveRunRef,
			Phase:      "create",
			Result:     runtimeops.ResultFailed,
			ReasonCode: runtimeops.ReasonCreateFailed,
			DurationMs: time.Since(createStart).Milliseconds(),
			Error:      err.Error(),
			Message:    "container create failed",
		})
		return err
	}

	ms.ContainerID = containerID

	sandboxID, _ := cm.engine.GetContainerSandboxID(containerID)
	if sandboxID != "" {
		_ = store.GetInstance().UpsertRuntimeContainerRef(ms.MicroserviceUUID, store.RuntimeScopeController, containerID, sandboxID)
	}

	cm.emitFromCM(ctx, runtimeops.RuntimeEvent{
		Event:       runtimeops.EventContainerCreated,
		MsUUID:      ms.MicroserviceUUID,
		ContainerID: containerID,
		SandboxID:   sandboxID,
		Image:       effectiveRunRef,
		Phase:       "create",
		Result:      runtimeops.ResultOK,
		DurationMs:  time.Since(createStart).Milliseconds(),
		Message:     "container created",
	})

	ip, err := cm.engine.GetContainerIPAddress(containerID)
	if err != nil {
		cm.logger.Warnf("Can't get IP address for container: %v", err)
		ip = "0.0.0.0"
	}
	ms.ContainerIPAddress = &ip

	cm.emitFromCM(ctx, runtimeops.RuntimeEvent{
		Event:       runtimeops.EventContainerStarting,
		MsUUID:      ms.MicroserviceUUID,
		ContainerID: containerID,
		SandboxID:   sandboxID,
		Image:       effectiveRunRef,
		Phase:       "start",
		Source:      runtimeops.SourceTask,
		Message:     "starting container",
	})
	startAt := time.Now()
	if err := cm.engine.StartContainer(containerID); err != nil {
		var nr *engine.NonRestartableContainerError
		startEvent := runtimeops.RuntimeEvent{
			Event:       runtimeops.EventContainerStarted,
			Level:       runtimeops.LevelWarn,
			MsUUID:      ms.MicroserviceUUID,
			ContainerID: containerID,
			SandboxID:   sandboxID,
			Image:       effectiveRunRef,
			Phase:       "start",
			Result:      runtimeops.ResultFailed,
			DurationMs:  time.Since(startAt).Milliseconds(),
			Message:     "container start failed",
		}
		if errors.As(err, &nr) {
			startEvent.ReasonCode = runtimeops.ReasonNonRestartableExit
			startEvent.Error = nr.Message
			startEvent.Fields = map[string]any{
				"exitCode": nr.ExitCode,
				"reason":   nr.Reason,
			}
		} else {
			startEvent.ReasonCode = runtimeops.ReasonStartFailed
			startEvent.Error = err.Error()
		}
		cm.emitFromCM(ctx, startEvent)
		return fmt.Errorf("failed to start container: %w", err)
	}
	cm.emitFromCM(ctx, runtimeops.RuntimeEvent{
		Event:       runtimeops.EventContainerStarted,
		MsUUID:      ms.MicroserviceUUID,
		ContainerID: containerID,
		SandboxID:   sandboxID,
		Image:       effectiveRunRef,
		Phase:       "start",
		Result:      runtimeops.ResultOK,
		DurationMs:  time.Since(startAt).Milliseconds(),
		Message:     "container started",
	})

	// Clear rebuild flag after successful creation
	ms.Rebuild = false

	// Set status to RUNNING via status reporter. Keep the previous current error
	// until the running grace window elapses.
	statusreporter.GetInstance().UpdateProcessManagerStatus(func(status *models.ProcessManagerStatus) {
		status.SetMicroservicesState(ms.MicroserviceUUID, models.MicroserviceStateRunning)
		// Set start time
		if msStatus := status.GetMicroserviceStatus(ms.MicroserviceUUID); msStatus != nil {
			msStatus.StartTime = time.Now().UnixMilli()
			msStatus.ContainerID = containerID
			msStatus.ApplyPodID(cm.engineName, sandboxID)
			if ms.ContainerIPAddress != nil {
				msStatus.IPAddress = ms.ContainerIPAddress
			}
		}
	})
	_ = cfg

	return nil
}

func (cm *ContainerManager) platformForPull(ms *models.Microservice) string {
	if ms == nil || ms.Platform == nil {
		return ""
	}
	return *ms.Platform
}

func (cm *ContainerManager) findFirstLocalImageRef(refs []string, registry *models.Registry) (string, bool, error) {
	registryURL, fromCache := imageref.MatchParamsOptional(registryURLFromRegistry(registry))
	for _, ref := range refs {
		exists, err := cm.engine.FindLocalImage(ref, registryURL, fromCache)
		if err != nil {
			return "", false, err
		}
		if exists {
			return ref, true, nil
		}
	}
	return "", false, nil
}
