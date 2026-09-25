package fieldagent

import (
	"fmt"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/eclipse-iofog/edgelet/pkg/imageref"
	"gopkg.in/yaml.v3"
)

func (fa *FieldAgent) reconcileControllerMicroservice(microservices []*models.Microservice) {
	if len(microservices) == 0 {
		return
	}
	db := store.GetInstance()
	if db.Conn() == nil {
		return
	}
	cp, found, err := db.GetSystemControlPlane()
	if err != nil || !found || cp == nil {
		return
	}
	cp.NormalizeDefaults()
	cpUUID := strings.TrimSpace(cp.ControllerUUID)
	if cpUUID == "" {
		return
	}

	var controllerMS *models.Microservice
	for _, ms := range microservices {
		if ms == nil {
			continue
		}
		if strings.TrimSpace(ms.MicroserviceUUID) != cpUUID {
			continue
		}
		if ms.IsController {
			controllerMS = ms
			break
		}
		controllerMS = ms
		ms.IsController = true
	}
	if controllerMS == nil {
		return
	}

	if controllerMS.Delete {
		logging.LogWarn(moduleName, fmt.Sprintf(
			"ignoring delete request for controller microservice uuid=%s while control plane deployment exists",
			cpUUID,
		))
		controllerMS.Delete = false
	}

	changed := false
	pullOnRecreate := false
	if controllerMS.Rebuild {
		controllerMS.Rebuild = false
		if fa.controllerRegister != nil &&
			fa.controllerRegister.isSucceeded(cpUUID) &&
			!fa.controllerRegister.isInitialRebuildSkipped(cpUUID) {
			fa.controllerRegister.markInitialRebuildSkipped(cpUUID)
			fa.persistControllerInitialRebuildSkipped(cpUUID)
			logging.LogDebug(moduleName, fmt.Sprintf(
				"skipping initial controller rebuild after register uuid=%s",
				cpUUID,
			))
		} else {
			changed = true
			pullOnRecreate = true
		}
	}

	potImage := strings.TrimSpace(controllerMS.ImageName)
	cpImage := strings.TrimSpace(cp.Image)
	if potImage != "" && cpImage != "" {
		doc, parseErr := models.ParseControlPlaneManifest(cp.ManifestYAML)
		if parseErr != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("controller reconcile manifest parse failed: %v", parseErr))
			return
		}
		registry := resolveControllerReconcileRegistry(controllerMS.RegistryID, doc)
		registryURL, fromCache := imageref.MatchParamsOptional(registry.URL)
		imageChanged := !imageref.Match(potImage, cpImage, registryURL, fromCache)
		registryChanged := controllerRegistryDrift(controllerMS.RegistryID, doc)
		if imageChanged || registryChanged {
			if imageChanged {
				doc.Spec.Controller.Image = potImage
				cp.Image = potImage
			}
			if registryChanged {
				regID := controllerMS.RegistryID
				doc.Spec.Controller.Registry = &regID
			}
			manifestYAML, marshalErr := yaml.Marshal(doc)
			if marshalErr != nil {
				logging.LogWarn(moduleName, fmt.Sprintf("controller reconcile manifest marshal failed: %v", marshalErr))
				return
			}
			cp.ManifestYAML = string(manifestYAML)
			changed = true
		}
	}

	if !changed {
		return
	}

	cp.Generation++
	cp.LastTransitionAt = cp.LastReconcileAt
	if err := db.UpsertSystemControlPlane(cp); err != nil {
		logging.LogWarn(moduleName, fmt.Sprintf("controller reconcile persist failed uuid=%s err=%v", cpUUID, err))
		return
	}
	if pullOnRecreate && fa.processManager != nil {
		fa.processManager.SetControlPlanePullOnRecreate(true)
	}
	if fa.processManager != nil {
		fa.processManager.MarkReconcile(cpUUID)
	}
	logging.LogInfo(moduleName, fmt.Sprintf("merged controller microservice spec uuid=%s generation=%d", cpUUID, cp.Generation))
}

func resolveControllerReconcileRegistry(potRegistryID int, doc *models.ControlPlaneManifest) *models.Registry {
	regID := potRegistryID
	if regID <= 0 && doc != nil {
		if id, ok := doc.ControllerRegistryID(); ok {
			regID = id
		}
	}
	if regID > 0 {
		if reg, err := store.GetInstance().GetLocalRegistry(regID); err == nil && reg != nil {
			return reg
		}
	}
	return models.NewRegistry(2, "from_cache", true, "", "", "")
}

func controllerRegistryDrift(potRegistryID int, doc *models.ControlPlaneManifest) bool {
	if potRegistryID <= 0 || doc == nil {
		return false
	}
	manifestRegID, ok := doc.ControllerRegistryID()
	if !ok {
		return true
	}
	return potRegistryID != manifestRegID
}
