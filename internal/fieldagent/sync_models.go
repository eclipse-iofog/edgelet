package fieldagent

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
)

// loadModels loads fleet-desired Models from the store or from the controller.
func (fa *FieldAgent) loadModels(fromFile bool) error {
	logging.LogDebug(moduleName, "get models")

	if fa.NotProvisioned() || !fa.IsControllerConnected(fromFile) {
		return nil
	}

	var items []*models.ControllerModel

	if fromFile {
		stored, err := loadControllerModelsFromStore()
		if err != nil || len(stored) == 0 {
			return fa.loadModels(false)
		}
		items = stored
		logging.LogDebug(moduleName, fmt.Sprintf("Loaded %d models from store", len(items)))
	} else {
		ctx, cancel := context.WithTimeout(fa.ctx, 30*time.Second)
		client := fa.getAPIClient()
		if client == nil {
			cancel()
			return errors.New("api client is not initialized")
		}
		result, err := client.Request(ctx, "models", GET, nil, nil)
		cancel()

		if err != nil {
			if isCertificateError(err) {
				fa.verificationFailed(err)
				return fmt.Errorf("unable to get models due to broken certificate: %w", err)
			}
			if IsControllerEndpointMissing(err) {
				logging.LogDebug(moduleName, "controller has no models endpoint; using empty model list")
				items = []*models.ControllerModel{}
			} else {
				return fmt.Errorf("unable to get models: %w", err)
			}
		} else {
			items = parseControllerModelList(result)
		}
	}

	ctx, cancel := context.WithTimeout(fa.ctx, 30*time.Second)
	err := fa.modelManager().ApplyControllerModels(ctx, items)
	cancel()
	if err != nil {
		return fmt.Errorf("unable to apply controller models: %w", err)
	}
	if !fromFile {
		fa.setModelLastUpdate(time.Now().Unix())
	}

	logging.LogDebug(moduleName, fmt.Sprintf("Finished get models (count: %d)", len(items)))
	return nil
}

func parseControllerModelList(result map[string]any) []*models.ControllerModel {
	items := make([]*models.ControllerModel, 0)
	rawList, ok := result["models"].([]any)
	if !ok {
		return items
	}
	for i, raw := range rawList {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		item, err := parseControllerModel(m)
		if err != nil {
			logging.LogError(moduleName, fmt.Sprintf("Unable to parse model at index %d: %v", i, err), err)
			continue
		}
		items = append(items, item)
	}
	return items
}

func parseControllerModel(data map[string]any) (*models.ControllerModel, error) {
	item := &models.ControllerModel{}
	if uuid, ok := data["uuid"].(string); ok {
		item.UUID = strings.TrimSpace(uuid)
	}
	if name, ok := data["name"].(string); ok {
		item.Name = strings.TrimSpace(name)
	}
	if repo, ok := data["repo"].(string); ok {
		item.Repo = repo
	}
	if revision, ok := data["revision"].(string); ok {
		item.Revision = revision
	}
	if format, ok := data["format"].(string); ok {
		item.Format = format
	}
	if registryID, ok := jsonInt(data["registryId"]); ok {
		item.RegistryID = registryID
	}
	if filesRaw, ok := data["files"]; ok {
		item.SetFiles(parseStringArray(filesRaw))
	}
	item.NormalizeDefaults()
	if item.UUID == "" {
		return nil, errors.New("model uuid is required")
	}
	if item.Name == "" {
		return nil, errors.New("model name is required")
	}
	return item, nil
}

func (fa *FieldAgent) fogModelStatus() (modelStatus string, activeModels int, modelLastUpdate int64) {
	defer func() {
		if r := recover(); r != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("model status for fog report panicked: %v", r))
			modelStatus = "[]"
			activeModels = 0
			modelLastUpdate = 0
		}
	}()

	modelStatus = "[]"
	db := store.GetInstance()
	if db == nil || db.Conn() == nil {
		return modelStatus, 0, 0
	}
	fleet, err := db.LoadControllerModels()
	if err != nil {
		fleet = nil
	}
	locals, err := db.ListLocalModels()
	if err != nil {
		locals = nil
	}
	localByName := make(map[string]*models.LocalModel, len(locals))
	for _, row := range locals {
		if row == nil {
			continue
		}
		localByName[row.Name] = row
	}

	payload := make([]map[string]any, 0, len(fleet)+len(locals))
	lastUpdate := fa.getModelLastUpdate()
	managedCount := 0
	managedNames := make(map[string]struct{}, len(fleet))
	for _, cm := range fleet {
		if cm == nil {
			continue
		}
		managedCount++
		managedNames[cm.Name] = struct{}{}
		item := map[string]any{
			"uuid":             cm.UUID,
			"name":             cm.Name,
			"source":           models.ModelSourceManaged,
			"state":            models.ModelStatePending,
			"digest":           "",
			"resolvedRevision": "",
			"revisionFloating": false,
			"totalBytes":       int64(0),
			"lastError":        "",
		}
		if row := localByName[cm.Name]; row != nil && row.Source == models.ModelSourceManaged {
			item["state"] = row.State
			item["digest"] = row.Digest
			item["resolvedRevision"] = row.ResolvedRevision
			item["revisionFloating"] = row.RevisionFloating
			item["totalBytes"] = row.TotalBytes
			item["lastError"] = row.LastError
			if row.LastReconcileAt > lastUpdate {
				lastUpdate = row.LastReconcileAt
			}
			if row.LastTransitionAt > lastUpdate {
				lastUpdate = row.LastTransitionAt
			}
		}
		payload = append(payload, item)
	}

	localOnly := make([]*models.LocalModel, 0)
	for _, row := range locals {
		if row == nil || row.Source != models.ModelSourceLocal {
			continue
		}
		if _, managed := managedNames[row.Name]; managed {
			continue
		}
		localOnly = append(localOnly, row)
	}
	slices.SortFunc(localOnly, func(a, b *models.LocalModel) int {
		return cmp.Compare(a.Name, b.Name)
	})
	for _, row := range localOnly {
		item := map[string]any{
			"name":             row.Name,
			"source":           models.ModelSourceLocal,
			"state":            row.State,
			"digest":           row.Digest,
			"resolvedRevision": row.ResolvedRevision,
			"revisionFloating": row.RevisionFloating,
			"totalBytes":       row.TotalBytes,
			"lastError":        row.LastError,
		}
		if row.LastReconcileAt > lastUpdate {
			lastUpdate = row.LastReconcileAt
		}
		if row.LastTransitionAt > lastUpdate {
			lastUpdate = row.LastTransitionAt
		}
		payload = append(payload, item)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return "[]", 0, 0
	}
	return string(raw), managedCount, lastUpdate
}
