package fieldagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/buildmeta"
	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/constants"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
)

// loadRuntimeClasses loads fleet-desired RuntimeClasses from the store or controller.
func (fa *FieldAgent) loadRuntimeClasses(fromFile bool) error {
	logging.LogDebug(moduleName, "get runtime classes")

	if fa.NotProvisioned() || !fa.IsControllerConnected(fromFile) {
		return nil
	}

	var items []*models.ControllerRuntimeClass

	if fromFile {
		stored, err := loadControllerRuntimeClassesFromStore()
		if err != nil || len(stored) == 0 {
			return fa.loadRuntimeClasses(false)
		}
		items = stored
		logging.LogDebug(moduleName, fmt.Sprintf("Loaded %d runtime classes from store", len(items)))
	} else {
		ctx, cancel := context.WithTimeout(fa.ctx, 30*time.Second)
		client := fa.getAPIClient()
		if client == nil {
			cancel()
			return errors.New("api client is not initialized")
		}
		result, err := client.Request(ctx, "runtimeClasses", GET, nil, nil)
		cancel()

		if err != nil {
			if isCertificateError(err) {
				fa.verificationFailed(err)
				return fmt.Errorf("unable to get runtime classes due to broken certificate: %w", err)
			}
			if IsControllerEndpointMissing(err) {
				logging.LogDebug(moduleName, "controller has no runtimeClasses endpoint; using empty runtime class list")
				items = []*models.ControllerRuntimeClass{}
			} else {
				return fmt.Errorf("unable to get runtime classes: %w", err)
			}
		} else {
			items = parseControllerRuntimeClassList(result)
		}
	}

	if err := saveControllerRuntimeClassesToStore(items); err != nil {
		return fmt.Errorf("unable to persist controller runtime classes: %w", err)
	}
	cfg := fa.config
	if cfg == nil {
		cfg = config.GetInstance()
	}
	if err := ApplyDesiredRuntimeClasses(cfg, items); err != nil {
		return fmt.Errorf("unable to apply controller runtime classes: %w", err)
	}

	logging.LogDebug(moduleName, fmt.Sprintf("Finished get runtime classes (count: %d)", len(items)))
	return nil
}

func parseControllerRuntimeClassList(result map[string]any) []*models.ControllerRuntimeClass {
	items := make([]*models.ControllerRuntimeClass, 0)
	if result == nil {
		return items
	}
	rawList, ok := result["runtimeClasses"].([]any)
	if !ok {
		return items
	}
	for i, raw := range rawList {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		item, err := parseControllerRuntimeClass(m)
		if err != nil {
			logging.LogError(moduleName, fmt.Sprintf("Unable to parse runtime class at index %d: %v", i, err), err)
			continue
		}
		items = append(items, item)
	}
	return items
}

func parseControllerRuntimeClass(data map[string]any) (*models.ControllerRuntimeClass, error) {
	item := &models.ControllerRuntimeClass{}
	if name, ok := data["name"].(string); ok {
		item.Name = strings.TrimSpace(name)
	}
	if handler, ok := data["handler"].(string); ok {
		item.Handler = strings.TrimSpace(handler)
	}
	item.NormalizeDefaults()
	if item.Name == "" {
		return nil, errors.New("runtime class name is required")
	}
	if item.Handler == "" {
		return nil, errors.New("runtime class handler is required")
	}
	doc := &models.LocalRuntimeClassManifest{
		APIVersion: "edgelet.iofog.org/v1",
		Kind:       "RuntimeClass",
		Handler:    item.Handler,
	}
	doc.Metadata.Name = item.Name
	if err := doc.Validate(); err != nil {
		return nil, err
	}
	item.Name = doc.Metadata.Name
	item.Handler = doc.Handler
	return item, nil
}

// ApplyDesiredRuntimeClasses applies fleet RuntimeClasses on the edgelet engine
// using the local persist path (source=managed). Docker and podman keep the
// desired snapshot and skip apply so the node stays healthy.
func ApplyDesiredRuntimeClasses(cfg *config.Config, items []*models.ControllerRuntimeClass) error {
	if !shouldApplyRuntimeClasses(cfg) {
		return nil
	}
	db := store.GetInstance()
	if db == nil || db.Conn() == nil {
		return errors.New("sqlite not open")
	}

	desired := make(map[string]*models.ControllerRuntimeClass, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		item.NormalizeDefaults()
		if item.Name == "" || item.Handler == "" {
			continue
		}
		if models.IsReservedRuntimeClassName(item.Name) {
			logging.LogWarn(moduleName, fmt.Sprintf("skipping reserved fleet runtime class %q", item.Name))
			continue
		}
		desired[item.Name] = item
		applied := &models.LocalRuntimeClass{
			Name:    item.Name,
			Handler: item.Handler,
			Source:  models.RuntimeClassSourceManaged,
		}
		if err := db.UpsertLocalRuntimeClass(applied); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("managed runtime class %s apply failed: %v", item.Name, err))
		}
	}

	existing, err := db.ListLocalRuntimeClasses()
	if err != nil {
		return fmt.Errorf("list applied runtime classes: %w", err)
	}
	for _, rc := range existing {
		if rc == nil || rc.Source != models.RuntimeClassSourceManaged {
			continue
		}
		if _, keep := desired[rc.Name]; keep {
			continue
		}
		if delErr := deleteDroppedManagedRuntimeClass(db, rc); delErr != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("managed runtime class %s delete skipped: %v", rc.Name, delErr))
		}
	}
	return nil
}

func shouldApplyRuntimeClasses(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	return buildmeta.HasEmbeddedEngine() && strings.EqualFold(strings.TrimSpace(cfg.ContainerEngine), constants.EngineEdgelet)
}

func deleteDroppedManagedRuntimeClass(db *store.DB, rc *models.LocalRuntimeClass) error {
	if db == nil || rc == nil {
		return nil
	}
	if models.IsReservedRuntimeClassName(rc.Name) {
		return fmt.Errorf("runtimeclass delete is not allowed for reserved runtime name: %s", rc.Name)
	}
	locals, err := db.ListLocalWorkloads()
	if err != nil {
		return fmt.Errorf("list local deployments while checking runtimeclass delete: %w", err)
	}
	controller, err := db.LoadControllerMicroservices()
	if err != nil {
		return fmt.Errorf("list controller microservices while checking runtimeclass delete: %w", err)
	}
	blocking := models.RuntimeClassBlockingUUIDs(rc, locals, controller)
	if len(blocking) > 0 {
		runtimeUsed := strings.TrimSpace(rc.RuntimeName)
		if runtimeUsed == "" {
			runtimeUsed = rc.Name
		}
		return fmt.Errorf("cannot delete runtimeclass '%s': microservice uuid=%s is still using runtime '%s'; delete dependent microservices first", rc.Name, blocking[0], runtimeUsed)
	}
	return db.DeleteLocalRuntimeClass(rc.Name)
}
