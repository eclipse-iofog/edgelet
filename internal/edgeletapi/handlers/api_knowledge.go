package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/knowledgemanager"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
)

func (h *EdgeletAPIHandler) HandleKnowledge(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/knowledge" {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
			return
		}
		items, err := h.facade.ListKnowledge()
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
			return
		}
		writeSuccess(w, http.StatusOK, map[string]any{
			"items": items,
			"count": len(items),
		})
		return
	}

	name := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/v1/knowledge/"))
	if name == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "knowledge name is required", nil)
		return
	}
	switch r.Method {
	case http.MethodGet:
		item, err := h.facade.GetKnowledge(name)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "knowledge not found", nil)
				return
			}
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
			return
		}
		writeSuccess(w, http.StatusOK, item)
	case http.MethodDelete:
		if err := h.facade.RemoveKnowledge(name); err != nil {
			if errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "not found") {
				writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, err.Error(), nil)
				return
			}
			if strings.Contains(err.Error(), "currently pulling") {
				writeAPIError(w, http.StatusConflict, ErrCodeConflict, err.Error(), nil)
				return
			}
			if strings.Contains(err.Error(), "bound") {
				writeAPIError(w, http.StatusConflict, ErrCodeConflict, err.Error(), nil)
				return
			}
			if strings.Contains(err.Error(), "required") {
				writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
				return
			}
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
			return
		}
		writeSuccess(w, http.StatusOK, map[string]any{
			"status": "ok",
			"name":   name,
		})
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
	}
}

func (h *EdgeletAPIHandler) HandleKnowledgePull(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	var req struct {
		Name       string   `json:"name"`
		Repo       string   `json:"repo,omitempty"`
		Revision   string   `json:"revision,omitempty"`
		RegistryID int      `json:"registryId,omitempty"`
		Files      []string `json:"files,omitempty"`
		Format     string   `json:"format,omitempty"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "invalid JSON body", nil)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "name is required", nil)
		return
	}
	repo := strings.TrimSpace(req.Repo)
	revision := strings.TrimSpace(req.Revision)
	format := strings.TrimSpace(req.Format)
	hasSpec := repo != "" || revision != "" || req.RegistryID > 0 || len(req.Files) > 0 || format != ""
	if hasSpec {
		if repo == "" || req.RegistryID <= 0 {
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "repo and registryId are required when specifying a knowledge", nil)
			return
		}
		if _, err := h.facade.UpsertLocalKnowledgeSpec(name, repo, revision, req.RegistryID, req.Files, format); err != nil {
			if errors.Is(err, knowledgemanager.ErrLocalKnowledgeDisabled) {
				writeAPIError(w, http.StatusConflict, ErrCodeConflict, err.Error(), nil)
				return
			}
			if strings.Contains(err.Error(), "not found") {
				writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, err.Error(), nil)
				return
			}
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
			return
		}
	}
	logging.LogInfo(apiHandlerModuleName, fmt.Sprintf("knowledge pull requested name=%s", name))
	op, err := h.facade.StartKnowledgePull(name)
	if err != nil {
		if errors.Is(err, knowledgemanager.ErrLocalKnowledgeDisabled) {
			writeAPIError(w, http.StatusConflict, ErrCodeConflict, err.Error(), nil)
			return
		}
		if strings.Contains(err.Error(), "not found") {
			writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, err.Error(), nil)
			return
		}
		if strings.Contains(err.Error(), "required") {
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
			return
		}
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
		return
	}
	writeSuccess(w, http.StatusAccepted, op.Snapshot())
}

func (h *EdgeletAPIHandler) HandleKnowledgePullStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	operationID := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/v1/knowledge:pull/"))
	if operationID == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "missing operation id", nil)
		return
	}
	op, ok := h.facade.GetKnowledgePull(operationID)
	if !ok {
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "pull operation not found", nil)
		return
	}
	writeSuccess(w, http.StatusOK, op.Snapshot())
}

func (h *EdgeletAPIHandler) HandleKnowledgePrune(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mode")))
	if mode != "" && mode != knowledgemanager.PruneModeDangling {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "knowledge prune supports only mode=dangling", nil)
		return
	}
	result, err := h.facade.PruneKnowledge(mode)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "invalid knowledge prune mode") {
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
			return
		}
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
		return
	}
	writeSuccess(w, http.StatusOK, result)
}

func (h *EdgeletAPIHandler) HandleDeployKnowledgeApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	manifest, _, dryRun, err := parseManifestMultipartRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
		return
	}
	logging.LogInfo(apiHandlerModuleName, fmt.Sprintf("local knowledge apply requested dryRun=%v", dryRun))
	rows, err := h.facade.ApplyLocalKnowledgeManifests(manifest, dryRun)
	if err != nil {
		logging.LogWarn(apiHandlerModuleName, fmt.Sprintf("local knowledge apply failed dryRun=%v err=%v", dryRun, err))
		if errors.Is(err, knowledgemanager.ErrLocalKnowledgeDisabled) {
			writeAPIError(w, http.StatusConflict, ErrCodeConflict, err.Error(), nil)
			return
		}
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
		return
	}
	payload := map[string]any{
		"accepted":  true,
		"dryRun":    dryRun,
		"kind":      "Knowledge",
		"count":     len(rows),
		"knowledge": knowledgeRowsToAPI(rows),
	}
	if len(rows) == 1 {
		payload["name"] = rows[0].Name
	}
	if !dryRun {
		payload["pulls"] = startAppliedKnowledgePulls(h, rows)
	}
	logging.LogInfo(apiHandlerModuleName, fmt.Sprintf("local knowledge apply succeeded count=%d dryRun=%v", len(rows), dryRun))
	writeSuccess(w, http.StatusOK, payload)
}

func startAppliedKnowledgePulls(h *EdgeletAPIHandler, rows []*models.LocalKnowledge) []map[string]any {
	pulls := make([]map[string]any, 0, len(rows))
	if h == nil {
		return pulls
	}
	for _, row := range rows {
		if row == nil || strings.TrimSpace(row.Name) == "" {
			continue
		}
		op, err := h.facade.StartKnowledgePull(row.Name)
		if err != nil {
			pulls = append(pulls, map[string]any{
				"name":  row.Name,
				"error": err.Error(),
			})
			continue
		}
		if op == nil {
			continue
		}
		pulls = append(pulls, op.Snapshot())
	}
	return pulls
}

func (h *EdgeletAPIHandler) HandleDeployKnowledgeValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	manifest, _, _, err := parseManifestMultipartRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
		return
	}
	docs, err := h.facade.ParseAndValidateLocalKnowledgeManifests(manifest)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
		return
	}
	names := make([]string, 0, len(docs))
	for _, doc := range docs {
		names = append(names, strings.TrimSpace(doc.Metadata.Name))
	}
	payload := map[string]any{
		"valid":      true,
		"apiVersion": docs[0].APIVersion,
		"kind":       docs[0].Kind,
		"name":       docs[0].Metadata.Name,
		"count":      len(docs),
	}
	if len(names) > 1 {
		payload["names"] = names
	}
	writeSuccess(w, http.StatusOK, payload)
}

func knowledgeRowsToAPI(rows []*models.LocalKnowledge) []map[string]any {
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, knowledgeRowToAPI(row))
	}
	return items
}

func knowledgeRowToAPI(row *models.LocalKnowledge) map[string]any {
	if row == nil {
		return map[string]any{}
	}
	source := strings.TrimSpace(row.Source)
	if source == "" {
		source = models.KnowledgeSourceLocal
	}
	item := map[string]any{
		"name":             row.Name,
		"source":           source,
		"repo":             row.Repo,
		"revision":         row.Revision,
		"registryId":       row.RegistryID,
		"files":            row.Files(),
		"format":           row.Format,
		"state":            row.State,
		"generation":       row.Generation,
		"revisionFloating": row.RevisionFloating,
		"totalBytes":       row.TotalBytes,
	}
	if strings.TrimSpace(row.LastError) != "" {
		item["lastError"] = row.LastError
	}
	if strings.TrimSpace(row.ResolvedRevision) != "" {
		item["resolvedRevision"] = row.ResolvedRevision
	}
	if strings.TrimSpace(row.Digest) != "" {
		item["digest"] = row.Digest
	}
	return item
}
