package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/modelmanager"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
)

func (h *EdgeletAPIHandler) HandleModels(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/models" {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
			return
		}
		items, err := h.facade.ListModels()
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

	name := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/v1/models/"))
	if name == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "model name is required", nil)
		return
	}
	switch r.Method {
	case http.MethodGet:
		item, err := h.facade.GetModel(name)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "model not found", nil)
				return
			}
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
			return
		}
		writeSuccess(w, http.StatusOK, item)
	case http.MethodDelete:
		if err := h.facade.RemoveModel(name); err != nil {
			if errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "not found") {
				writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, err.Error(), nil)
				return
			}
			if strings.Contains(err.Error(), "currently pulling") {
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

func (h *EdgeletAPIHandler) HandleModelPull(w http.ResponseWriter, r *http.Request) {
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
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "repo and registryId are required when specifying a model", nil)
			return
		}
		if _, err := h.facade.UpsertLocalModelSpec(name, repo, revision, req.RegistryID, req.Files, format); err != nil {
			if errors.Is(err, modelmanager.ErrLocalModelsDisabled) {
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
	logging.LogInfo(apiHandlerModuleName, fmt.Sprintf("model pull requested name=%s", name))
	op, err := h.facade.StartModelPull(name)
	if err != nil {
		if errors.Is(err, modelmanager.ErrLocalModelsDisabled) {
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

func (h *EdgeletAPIHandler) HandleModelPullStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	operationID := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/v1/models:pull/"))
	if operationID == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "missing operation id", nil)
		return
	}
	op, ok := h.facade.GetModelPull(operationID)
	if !ok {
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "pull operation not found", nil)
		return
	}
	writeSuccess(w, http.StatusOK, op.Snapshot())
}

func (h *EdgeletAPIHandler) HandleModelPrune(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mode")))
	if mode != "" && mode != modelmanager.PruneModeDangling {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "model prune supports only mode=dangling", nil)
		return
	}
	result, err := h.facade.PruneModels(mode)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "invalid model prune mode") {
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
			return
		}
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
		return
	}
	writeSuccess(w, http.StatusOK, result)
}

func (h *EdgeletAPIHandler) HandleDeployModelsApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	manifest, _, dryRun, err := parseManifestMultipartRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
		return
	}
	logging.LogInfo(apiHandlerModuleName, fmt.Sprintf("local model apply requested dryRun=%v", dryRun))
	rows, err := h.facade.ApplyLocalModelManifests(manifest, dryRun)
	if err != nil {
		logging.LogWarn(apiHandlerModuleName, fmt.Sprintf("local model apply failed dryRun=%v err=%v", dryRun, err))
		if errors.Is(err, modelmanager.ErrLocalModelsDisabled) {
			writeAPIError(w, http.StatusConflict, ErrCodeConflict, err.Error(), nil)
			return
		}
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
		return
	}
	payload := map[string]any{
		"accepted": true,
		"dryRun":   dryRun,
		"kind":     "Model",
		"count":    len(rows),
		"models":   rowsToAPI(rows),
	}
	if len(rows) == 1 {
		payload["model"] = modelRowToAPI(rows[0])
		payload["name"] = rows[0].Name
	}
	if !dryRun {
		payload["pulls"] = startAppliedModelPulls(h, rows)
	}
	logging.LogInfo(apiHandlerModuleName, fmt.Sprintf("local model apply succeeded count=%d dryRun=%v", len(rows), dryRun))
	writeSuccess(w, http.StatusOK, payload)
}

func startAppliedModelPulls(h *EdgeletAPIHandler, rows []*models.LocalModel) []map[string]any {
	pulls := make([]map[string]any, 0, len(rows))
	if h == nil {
		return pulls
	}
	for _, row := range rows {
		if row == nil || strings.TrimSpace(row.Name) == "" {
			continue
		}
		op, err := h.facade.StartModelPull(row.Name)
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

func (h *EdgeletAPIHandler) HandleDeployModelsValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	manifest, _, _, err := parseManifestMultipartRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
		return
	}
	docs, err := h.facade.ParseAndValidateLocalModelManifests(manifest)
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

func rowsToAPI(rows []*models.LocalModel) []map[string]any {
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, modelRowToAPI(row))
	}
	return items
}

func modelRowToAPI(row *models.LocalModel) map[string]any {
	if row == nil {
		return map[string]any{}
	}
	source := strings.TrimSpace(row.Source)
	if source == "" {
		source = models.ModelSourceLocal
	}
	item := map[string]any{
		"name":               row.Name,
		"source":             source,
		"repo":               row.Repo,
		"revision":           row.Revision,
		"registryId":         row.RegistryID,
		"files":              row.Files(),
		"format":             row.Format,
		"state":              row.State,
		"generation":         row.Generation,
		"observedGeneration": row.ObservedGeneration,
		"revisionFloating":   row.RevisionFloating,
		"totalBytes":         row.TotalBytes,
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
