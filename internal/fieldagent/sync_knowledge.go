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

// loadKnowledge loads fleet-desired Knowledge from the store or from the controller.
func (fa *FieldAgent) loadKnowledge(fromFile bool) error {
	logging.LogDebug(moduleName, "get knowledge")

	if fa.NotProvisioned() || !fa.IsControllerConnected(fromFile) {
		return nil
	}

	var items []*models.ControllerKnowledge

	if fromFile {
		stored, err := loadControllerKnowledgeFromStore()
		if err != nil || len(stored) == 0 {
			return fa.loadKnowledge(false)
		}
		items = stored
		logging.LogDebug(moduleName, fmt.Sprintf("Loaded %d knowledge from store", len(items)))
	} else {
		ctx, cancel := context.WithTimeout(fa.ctx, 30*time.Second)
		client := fa.getAPIClient()
		if client == nil {
			cancel()
			return errors.New("api client is not initialized")
		}
		result, err := client.Request(ctx, "knowledge", GET, nil, nil)
		cancel()

		if err != nil {
			if isCertificateError(err) {
				fa.verificationFailed(err)
				return fmt.Errorf("unable to get knowledge due to broken certificate: %w", err)
			}
			if IsControllerEndpointMissing(err) {
				logging.LogDebug(moduleName, "controller has no knowledge endpoint; using empty knowledge list")
				items = []*models.ControllerKnowledge{}
			} else {
				return fmt.Errorf("unable to get knowledge: %w", err)
			}
		} else {
			items = parseControllerKnowledgeList(result)
		}
	}

	ctx, cancel := context.WithTimeout(fa.ctx, 30*time.Second)
	err := fa.knowledgeManager().ApplyControllerKnowledge(ctx, items)
	cancel()
	if err != nil {
		return fmt.Errorf("unable to apply controller knowledge: %w", err)
	}
	if !fromFile {
		fa.setKnowledgeLastUpdate(time.Now().UnixMilli())
	}

	logging.LogDebug(moduleName, fmt.Sprintf("Finished get knowledge (count: %d)", len(items)))
	return nil
}

func parseControllerKnowledgeList(result map[string]any) []*models.ControllerKnowledge {
	items := make([]*models.ControllerKnowledge, 0)
	rawList, ok := result["knowledge"].([]any)
	if !ok {
		return items
	}
	for i, raw := range rawList {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		item, err := parseControllerKnowledge(m)
		if err != nil {
			logging.LogError(moduleName, fmt.Sprintf("Unable to parse knowledge at index %d: %v", i, err), err)
			continue
		}
		items = append(items, item)
	}
	return items
}

func parseControllerKnowledge(data map[string]any) (*models.ControllerKnowledge, error) {
	item := &models.ControllerKnowledge{}
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
		return nil, errors.New("knowledge uuid is required")
	}
	if item.Name == "" {
		return nil, errors.New("knowledge name is required")
	}
	return item, nil
}

func (fa *FieldAgent) fogKnowledgeStatus() (knowledgeStatus string, activeKnowledge int, knowledgeLastUpdate int64) {
	defer func() {
		if r := recover(); r != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("knowledge status for fog report panicked: %v", r))
			knowledgeStatus = "[]"
			activeKnowledge = 0
			knowledgeLastUpdate = 0
		}
	}()

	knowledgeStatus = "[]"
	db := store.GetInstance()
	if db == nil || db.Conn() == nil {
		return knowledgeStatus, 0, 0
	}
	fleet, err := db.LoadControllerKnowledge()
	if err != nil {
		fleet = nil
	}
	locals, err := db.ListLocalKnowledge()
	if err != nil {
		locals = nil
	}
	localByName := make(map[string]*models.LocalKnowledge, len(locals))
	for _, row := range locals {
		if row == nil {
			continue
		}
		localByName[row.Name] = row
	}

	payload := make([]map[string]any, 0, len(fleet)+len(locals))
	lastUpdate := fa.getKnowledgeLastUpdate()
	managedCount := 0
	managedNames := make(map[string]struct{}, len(fleet))
	for _, ck := range fleet {
		if ck == nil {
			continue
		}
		managedCount++
		managedNames[ck.Name] = struct{}{}
		item := map[string]any{
			"uuid":             ck.UUID,
			"name":             ck.Name,
			"source":           models.KnowledgeSourceManaged,
			"state":            models.KnowledgeStatePending,
			"digest":           "",
			"resolvedRevision": "",
			"revisionFloating": false,
			"totalBytes":       int64(0),
			"lastError":        "",
		}
		if row := localByName[ck.Name]; row != nil && row.Source == models.KnowledgeSourceManaged {
			item["state"] = row.State
			item["digest"] = row.Digest
			item["resolvedRevision"] = row.ResolvedRevision
			item["revisionFloating"] = row.RevisionFloating
			item["totalBytes"] = row.TotalBytes
			item["lastError"] = row.LastError
		}
		payload = append(payload, item)
	}

	localOnly := make([]*models.LocalKnowledge, 0)
	for _, row := range locals {
		if row == nil || row.Source != models.KnowledgeSourceLocal {
			continue
		}
		if _, managed := managedNames[row.Name]; managed {
			continue
		}
		localOnly = append(localOnly, row)
	}
	slices.SortFunc(localOnly, func(a, b *models.LocalKnowledge) int {
		return cmp.Compare(a.Name, b.Name)
	})
	for _, row := range localOnly {
		item := map[string]any{
			"name":             row.Name,
			"source":           models.KnowledgeSourceLocal,
			"state":            row.State,
			"digest":           row.Digest,
			"resolvedRevision": row.ResolvedRevision,
			"revisionFloating": row.RevisionFloating,
			"totalBytes":       row.TotalBytes,
			"lastError":        row.LastError,
		}
		payload = append(payload, item)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return "[]", 0, 0
	}
	return string(raw), managedCount, lastUpdate
}
