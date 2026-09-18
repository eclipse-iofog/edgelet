package processmanager

import (
	"errors"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/modelcatalog"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/store"
)

func (pm *ProcessManager) catalogDiskDirectoryValue() string {
	if pm != nil && strings.TrimSpace(pm.catalogDiskDirectory) != "" {
		return strings.TrimSpace(pm.catalogDiskDirectory)
	}
	if cfg := config.GetInstance(); cfg != nil {
		return strings.TrimSpace(cfg.DiskDirectory)
	}
	return ""
}

func requiredModelSource(msUUID string) string {
	db := store.GetInstance()
	if db == nil || db.Conn() == nil {
		return models.ModelSourceManaged
	}
	item, err := db.GetLocalWorkload(msUUID)
	if err == nil && item != nil {
		return models.ModelSourceLocal
	}
	return models.ModelSourceManaged
}

func (pm *ProcessManager) prepareCatalog(ms *models.Microservice, forStart bool) (*modelcatalog.PrepareResult, error) {
	if ms == nil {
		return nil, errors.New("microservice is nil")
	}
	disk := pm.catalogDiskDirectoryValue()
	if pm.containerManager != nil && strings.TrimSpace(pm.containerManager.catalogDiskDirectory) != "" {
		disk = strings.TrimSpace(pm.containerManager.catalogDiskDirectory)
	}
	return modelcatalog.Prepare(disk, store.GetInstance(), ms, requiredModelSource(ms.MicroserviceUUID), forStart)
}

func (pm *ProcessManager) applyCatalogStartGate(ms *models.Microservice) (proceed bool) {
	if ms == nil {
		return true
	}
	if !ms.Models.HasItems() {
		pm.releaseCatalog(ms.MicroserviceUUID)
		return true
	}
	res, err := pm.prepareCatalog(ms, true)
	if err != nil {
		pm.logger.Warnf("catalog start gate for %s: %v", ms.MicroserviceUUID, err)
		statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
			s.SetMicroservicesState(ms.MicroserviceUUID, models.MicroserviceStateFailed)
			s.SetMicroservicesStatusErrorMessage(ms.MicroserviceUUID, err.Error())
		})
		return false
	}
	switch res.Decision {
	case models.CatalogGateWait:
		GetRestartStuckChecker().MarkCatalogWaiting(ms.MicroserviceUUID)
		statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
			s.SetMicroservicesState(ms.MicroserviceUUID, models.MicroserviceStateQueued)
			s.SetMicroservicesStatusErrorMessage(ms.MicroserviceUUID, res.Message)
		})
		return false
	case models.CatalogGateFail:
		statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
			s.SetMicroservicesState(ms.MicroserviceUUID, models.MicroserviceStateFailed)
			s.SetMicroservicesStatusErrorMessage(ms.MicroserviceUUID, res.Message)
		})
		return false
	default:
		return true
	}
}

func (pm *ProcessManager) refreshCatalogProjection(ms *models.Microservice) {
	if ms == nil {
		return
	}
	res, err := pm.prepareCatalog(ms, false)
	if err != nil {
		pm.logger.Warnf("catalog refresh for %s: %v", ms.MicroserviceUUID, err)
		return
	}
	// Item add/remove on an existing catalog stays in-place. Empty↔nonempty,
	// bindPath, and catalog permissions still recreate.
	if res.MountChanged {
		ms.Rebuild = true
	}
}

func (pm *ProcessManager) releaseCatalog(msUUID string) {
	disk := pm.catalogDiskDirectoryValue()
	if pm.containerManager != nil && strings.TrimSpace(pm.containerManager.catalogDiskDirectory) != "" {
		disk = strings.TrimSpace(pm.containerManager.catalogDiskDirectory)
	}
	if err := modelcatalog.Release(disk, store.GetInstance(), msUUID); err != nil {
		pm.logger.Warnf("catalog release for %s: %v", msUUID, err)
	}
}

func (cm *ContainerManager) catalogDiskDirectoryValue() string {
	if cm != nil && strings.TrimSpace(cm.catalogDiskDirectory) != "" {
		return strings.TrimSpace(cm.catalogDiskDirectory)
	}
	if cfg := config.GetInstance(); cfg != nil {
		return strings.TrimSpace(cfg.DiskDirectory)
	}
	return ""
}

func (cm *ContainerManager) applyCatalogStartGate(ms *models.Microservice) error {
	if ms == nil {
		return nil
	}
	if !ms.Models.HasItems() {
		cm.releaseCatalog(ms.MicroserviceUUID)
		return nil
	}
	res, err := modelcatalog.Prepare(cm.catalogDiskDirectoryValue(), store.GetInstance(), ms, requiredModelSource(ms.MicroserviceUUID), true)
	if err != nil {
		return err
	}
	return modelcatalog.WrapDecision(res)
}

func (cm *ContainerManager) releaseCatalog(msUUID string) {
	if strings.TrimSpace(msUUID) == "" {
		return
	}
	_ = modelcatalog.Release(cm.catalogDiskDirectoryValue(), store.GetInstance(), msUUID)
}

func (pm *ProcessManager) gateLocalCatalog(ms *models.Microservice) error {
	if ms == nil {
		return nil
	}
	if !ms.Models.HasItems() {
		pm.releaseCatalog(ms.MicroserviceUUID)
		return nil
	}
	res, err := pm.prepareCatalog(ms, true)
	if err != nil {
		return err
	}
	return modelcatalog.WrapDecision(res)
}

func (pm *ProcessManager) refreshLocalCatalogIfNeeded(item *models.LocalDeployedMicroservice, now int64) bool {
	if item == nil {
		return false
	}
	doc, err := decodeLocalDeployManifest(item.ManifestYAML)
	if err != nil {
		return false
	}
	image := doc.ManifestImage()
	localMS := models.BuildMicroserviceFromLocalManifest(doc, item.LocalUUID, image)
	res, err := pm.prepareCatalog(localMS, false)
	if err != nil {
		pm.logger.Warnf("local catalog refresh for %s: %v", item.LocalUUID, err)
		return false
	}
	if res.MountChanged {
		if recErr := pm.recreateLocalDeployment(item, false, now); recErr != nil {
			pm.logger.Warnf("local catalog bindPath recreate for %s: %v", item.LocalUUID, recErr)
		}
		return true
	}
	return false
}

func normalizeCatalogTaskError(msUUID string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, modelcatalog.ErrWaiting) {
		statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
			s.SetMicroservicesState(msUUID, models.MicroserviceStateQueued)
			s.SetMicroservicesStatusErrorMessage(msUUID, models.CatalogWaitingMessage)
		})
		return nil
	}
	if errors.Is(err, modelcatalog.ErrFailed) {
		statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
			s.SetMicroservicesState(msUUID, models.MicroserviceStateFailed)
			s.SetMicroservicesStatusErrorMessage(msUUID, err.Error())
		})
		return nil
	}
	return err
}
