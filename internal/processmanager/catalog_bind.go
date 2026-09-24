package processmanager

import (
	"errors"
	"fmt"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/knowledgecatalog"
	"github.com/eclipse-iofog/edgelet/internal/modelcatalog"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/store"
)

type catalogPrepareResult struct {
	Decision     models.CatalogGateDecision
	Message      string
	MountChanged bool
}

func (pm *ProcessManager) catalogDiskDirectoryValue() string {
	if pm != nil && strings.TrimSpace(pm.catalogDiskDirectory) != "" {
		return strings.TrimSpace(pm.catalogDiskDirectory)
	}
	if cfg := config.GetInstance(); cfg != nil {
		return strings.TrimSpace(cfg.DiskDirectory)
	}
	return ""
}

func requiredCatalogSource(msUUID string) string {
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

func hasCatalogItems(ms *models.Microservice) bool {
	return ms != nil && (ms.Models.HasItems() || ms.Knowledge.HasItems())
}

func (pm *ProcessManager) prepareCatalog(ms *models.Microservice, forStart bool) (*catalogPrepareResult, error) {
	if ms == nil {
		return nil, errors.New("microservice is nil")
	}
	disk := pm.catalogDiskDirectoryValue()
	if pm.containerManager != nil && strings.TrimSpace(pm.containerManager.catalogDiskDirectory) != "" {
		disk = strings.TrimSpace(pm.containerManager.catalogDiskDirectory)
	}
	return prepareWorkloadCatalogs(disk, store.GetInstance(), ms, requiredCatalogSource(ms.MicroserviceUUID), forStart)
}

func prepareWorkloadCatalogs(diskDirectory string, db *store.DB, ms *models.Microservice, requiredSource string, forStart bool) (*catalogPrepareResult, error) {
	modelsRes, err := modelcatalog.Prepare(diskDirectory, db, ms, requiredSource, forStart)
	if err != nil {
		return nil, err
	}
	knowledgeRes, err := knowledgecatalog.Prepare(diskDirectory, db, ms, requiredSource, forStart)
	if err != nil {
		return nil, err
	}
	dec, msg := models.CombineCatalogStartGates(modelsRes.Decision, modelsRes.Message, knowledgeRes.Decision, knowledgeRes.Message)
	return &catalogPrepareResult{
		Decision:     dec,
		Message:      msg,
		MountChanged: modelsRes.MountChanged || knowledgeRes.MountChanged,
	}, nil
}

func wrapCombinedCatalogDecision(res *catalogPrepareResult) error {
	if res == nil {
		return nil
	}
	switch res.Decision {
	case models.CatalogGateWait:
		if strings.TrimSpace(res.Message) == "" || res.Message == models.CatalogWaitingMessage || res.Message == models.KnowledgeCatalogWaitingMessage {
			return modelcatalog.ErrWaiting
		}
		return fmt.Errorf("%w: %s", modelcatalog.ErrWaiting, res.Message)
	case models.CatalogGateFail:
		if strings.TrimSpace(res.Message) == "" {
			return modelcatalog.ErrFailed
		}
		return fmt.Errorf("%w: %s", modelcatalog.ErrFailed, res.Message)
	default:
		return nil
	}
}

func (pm *ProcessManager) applyCatalogStartGate(ms *models.Microservice) (proceed bool) {
	if ms == nil {
		return true
	}
	if !hasCatalogItems(ms) {
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
	db := store.GetInstance()
	if err := modelcatalog.Release(disk, db, msUUID); err != nil {
		pm.logger.Warnf("catalog release for %s: %v", msUUID, err)
	}
	if err := knowledgecatalog.Release(disk, db, msUUID); err != nil {
		pm.logger.Warnf("knowledge catalog release for %s: %v", msUUID, err)
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
	if !hasCatalogItems(ms) {
		cm.releaseCatalog(ms.MicroserviceUUID)
		return nil
	}
	res, err := prepareWorkloadCatalogs(cm.catalogDiskDirectoryValue(), store.GetInstance(), ms, requiredCatalogSource(ms.MicroserviceUUID), true)
	if err != nil {
		return err
	}
	return wrapCombinedCatalogDecision(res)
}

func (cm *ContainerManager) releaseCatalog(msUUID string) {
	if strings.TrimSpace(msUUID) == "" {
		return
	}
	disk := cm.catalogDiskDirectoryValue()
	db := store.GetInstance()
	_ = modelcatalog.Release(disk, db, msUUID)
	_ = knowledgecatalog.Release(disk, db, msUUID)
}

func (pm *ProcessManager) gateLocalCatalog(ms *models.Microservice) error {
	if ms == nil {
		return nil
	}
	if !hasCatalogItems(ms) {
		pm.releaseCatalog(ms.MicroserviceUUID)
		return nil
	}
	res, err := pm.prepareCatalog(ms, true)
	if err != nil {
		return err
	}
	return wrapCombinedCatalogDecision(res)
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
			s.SetMicroservicesStatusErrorMessage(msUUID, catalogWaitStatusText(err))
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

func catalogWaitStatusText(err error) string {
	if err == nil {
		return models.CatalogWaitingMessage
	}
	text := strings.TrimSpace(err.Error())
	prefix := modelcatalog.ErrWaiting.Error()
	if text == "" || text == prefix {
		return models.CatalogWaitingMessage
	}
	if trimmed := strings.TrimSpace(strings.TrimPrefix(text, prefix+":")); trimmed != "" && trimmed != text {
		return trimmed
	}
	if trimmed := strings.TrimSpace(strings.TrimPrefix(text, prefix+"\n")); trimmed != "" && trimmed != text {
		return trimmed
	}
	return text
}
