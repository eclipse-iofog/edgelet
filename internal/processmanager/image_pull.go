package processmanager

import (
	"errors"
	"fmt"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/runtimeops"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/pkg/engine"
	"github.com/eclipse-iofog/edgelet/pkg/imageref"
)

// imagePullMissError is a registry download that left the image off this machine.
// Reconcile retries it. It is not a crash and it does not stop a running container.
type imagePullMissError struct {
	ref string
	err error
}

func (e *imagePullMissError) Error() string {
	cause := "image not found"
	ref := ""
	if e != nil {
		ref = strings.TrimSpace(e.ref)
		if e.err != nil && strings.TrimSpace(e.err.Error()) != "" {
			cause = e.err.Error()
		}
	}
	if ref == "" {
		return "unable to pull image: " + cause
	}
	return fmt.Sprintf("unable to pull %s: %s", ref, cause)
}

func (e *imagePullMissError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func newImagePullMiss(ref string, err error) error {
	return &imagePullMissError{ref: ref, err: err}
}

func isImagePullMiss(err error) bool {
	var miss *imagePullMissError
	return errors.As(err, &miss)
}

func reportImagePullMiss(uuid string, state models.MicroserviceState, message string) {
	if strings.TrimSpace(uuid) == "" {
		return
	}
	statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
		s.SetImagePullMiss(uuid, state, message)
	})
}

func clearImagePullStatus(uuid string) {
	if strings.TrimSpace(uuid) == "" {
		return
	}
	statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
		s.ClearImagePullStatus(uuid)
	})
}

// clearImagePullFailure drops the pull report once the desired image is running.
func clearImagePullFailure(uuid string) {
	uuid = strings.TrimSpace(uuid)
	if uuid == "" {
		return
	}
	tracked := GetRestartStuckChecker().HasPullTracking(uuid)
	statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
		s.ResolveImagePull(uuid, tracked)
	})
	GetRestartStuckChecker().ClearPullWait(uuid)
}

func (pm *ProcessManager) noteImagePullMiss(uuid string, cause error) {
	image := ""
	if pm != nil && pm.microserviceManager != nil {
		if ms := pm.microserviceManager.FindLatestMicroserviceByUUID(uuid); ms != nil {
			image = ms.ImageName
			ms.SetIsUpdating(false)
		}
	}
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	reportImagePullMiss(uuid, pm.runtimeStateAfterPullMiss(uuid), msg)
	pm.armImagePull(uuid, image)
}

func (pm *ProcessManager) armImagePull(uuid, image string) {
	delay := GetRestartStuckChecker().ArmPullMiss(uuid, image)
	if pm == nil {
		return
	}
	pm.armReconcileDeadline(uuid, delay)
}

func (pm *ProcessManager) delayImagePull(uuid, decision string) {
	if pm == nil {
		return
	}
	delay := GetRestartStuckChecker().RemainingPullDelay(uuid)
	pm.emitReconcileDecision(uuid, decision, "image_pull_backoff", "delaying image pull", runtimeops.LevelInfo, map[string]any{
		"delayMs": delay.Milliseconds(),
	})
	pm.armReconcileDeadline(uuid, delay)
}

func (pm *ProcessManager) consumeRebuildSkip(ms *models.Microservice, skip recreateSkip) {
	if ms == nil || skip != recreateSkipRebuild {
		return
	}
	GetRestartStuckChecker().ConsumeRebuildSkip(ms.MicroserviceUUID)
}

func (pm *ProcessManager) runtimeStateAfterPullMiss(uuid string) models.MicroserviceState {
	container := pm.containerAfterPullMiss(uuid)
	if container == nil {
		return models.MicroserviceStateQueued
	}
	if pm != nil && pm.engine != nil {
		if st, err := pm.engine.GetContainerStatus(container.ID, uuid); err == nil && st != nil && st.Status != "" && st.Status != models.MicroserviceStateUnknown {
			return st.Status
		}
	}
	return models.MicroserviceStateRunning
}

func (pm *ProcessManager) containerAfterPullMiss(uuid string) *engine.Container {
	if pm == nil || pm.containerManager == nil {
		return nil
	}
	container, err := pm.containerManager.GetContainerForMicroservice(uuid)
	if err != nil {
		return nil
	}
	return container
}

func (pm *ProcessManager) noteLocalImagePullMiss(item *models.LocalDeployedMicroservice, image string, cause error, containerUp bool) {
	if item == nil {
		return
	}
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	item.LastError = msg
	if containerUp {
		runtime := strings.ToLower(strings.TrimSpace(item.RuntimeState))
		if runtime == "" || runtime == "failed" || runtime == "starting" || runtime == "stuck_in_restart" {
			item.RuntimeState = "running"
		}
	} else {
		item.RuntimeState = "queued"
	}
	item.State = item.RuntimeState
	state := models.MicroserviceStateQueued
	if containerUp {
		state = models.MicroserviceStateRunning
	}
	reportImagePullMiss(item.LocalUUID, state, msg)
	if pm != nil {
		pm.armImagePull(item.LocalUUID, image)
	} else {
		GetRestartStuckChecker().ArmPullMiss(item.LocalUUID, image)
	}
	_ = persistLocalWorkloadIfPresent(item)
}

func (pm *ProcessManager) noteControlPlaneImagePullMiss(item *models.ControlPlaneDeployment, image string, cause error, containerUp bool) {
	if item == nil {
		return
	}
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	item.LastError = msg
	if containerUp {
		runtime := strings.ToLower(strings.TrimSpace(item.RuntimeState))
		if runtime == "" || runtime == "failed" || runtime == "starting" || runtime == "stuck_in_restart" {
			item.RuntimeState = "running"
		}
	} else {
		item.RuntimeState = "queued"
	}
	item.State = item.RuntimeState
	state := models.MicroserviceStateQueued
	if containerUp {
		state = models.MicroserviceStateRunning
	}
	reportImagePullMiss(item.ControllerUUID, state, msg)
	if pm != nil {
		pm.armImagePull(item.ControllerUUID, image)
	} else {
		GetRestartStuckChecker().ArmPullMiss(item.ControllerUUID, image)
	}
	_ = store.GetInstance().UpsertSystemControlPlane(item)
}

func localDesiredImage(item *models.LocalDeployedMicroservice) string {
	if item == nil {
		return ""
	}
	if doc, err := decodeLocalDeployManifest(item.ManifestYAML); err == nil && doc != nil {
		if image := strings.TrimSpace(doc.ManifestImage()); image != "" {
			return image
		}
	}
	return strings.TrimSpace(item.ImageName)
}

func controlPlaneDesiredImage(item *models.ControlPlaneDeployment) string {
	if item == nil {
		return ""
	}
	if doc, err := models.ParseControlPlaneManifest(item.ManifestYAML); err == nil && doc != nil {
		if image := strings.TrimSpace(doc.ManifestControllerImage()); image != "" {
			return image
		}
	}
	return strings.TrimSpace(item.Image)
}

// ensureControllerImage pulls when the registry is remote, then requires the
// image to be present. A miss leaves the caller to keep the current container.
func (pm *ProcessManager) ensureControllerImage(ms *models.Microservice, registry *models.Registry, pullRequested bool) error {
	if pm == nil || pm.engine == nil || ms == nil || registry == nil {
		return errors.New("container engine is not initialized")
	}
	if err := models.RequireOCIForImagePull(registry); err != nil {
		return err
	}
	pullRef, lookupRefs, fromCache := imageref.ResolveForRegistry(ms.ImageName, registry.URL)
	var pullErr error
	pullSucceeded := false
	if !fromCache {
		if err := pm.engine.PullImage(pullRef, registry, &engine.PullImageOptions{Platform: msPlatform(ms)}); err != nil {
			pullErr = err
			if pm.logger != nil {
				pm.logger.Warnf("unable to pull %s, trying local cache: %v", pullRef, err)
			}
		} else {
			pullSucceeded = true
		}
	} else if pullRequested && pm.logger != nil {
		pm.logger.Debugf("control plane image %s is resolved from the local cache", pullRef)
	}
	registryURL, lookupFromCache := imageref.MatchParamsOptional(registry.URL)
	matched := ""
	for _, ref := range lookupRefs {
		exists, err := pm.engine.FindLocalImage(ref, registryURL, lookupFromCache)
		if err != nil {
			return err
		}
		if exists {
			matched = ref
			break
		}
	}
	if matched == "" {
		if pullErr == nil {
			pullErr = fmt.Errorf("image not found in local cache for refs: %v", lookupRefs)
		}
		return newImagePullMiss(pullRef, pullErr)
	}
	if pullSucceeded {
		ms.ImageName = pullRef
		return nil
	}
	ms.ImageName = matched
	return nil
}
