package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/runtimeapi"
	"github.com/eclipse-iofog/edgelet/internal/volumereclaim"
)

func (h *EdgeletAPIHandler) HandleVolumes(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/volumes" {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
			return
		}
		items, err := h.facade.ListVolumeClaims()
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
			return
		}
		writeSuccess(w, http.StatusOK, map[string]any{
			"volumes": items,
			"count":   len(items),
		})
		return
	}

	uuid := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/v1/volumes/"))
	if uuid == "" || uuid == "shared" || strings.HasPrefix(uuid, "shared/") {
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "not found", nil)
		return
	}
	if r.Method != http.MethodDelete {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	force, err := parseBooleanFormValue(r.URL.Query().Get("force"), "force")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
		return
	}
	if err := h.facade.RemovePrivateVolume(uuid, name, force); err != nil {
		writeVolumeReclaimError(w, err)
		return
	}
	writeSuccess(w, http.StatusOK, map[string]any{
		"status": "ok",
		"uuid":   uuid,
		"scope":  "private",
	})
}

func (h *EdgeletAPIHandler) HandleVolumeShared(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/v1/volumes/shared/"))
	if name == "" {
		name = strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/v1/volumes/shared"))
	}
	name, _ = url.PathUnescape(name)
	name = strings.Trim(strings.TrimSpace(name), "/")
	if name == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "shared volume name is required", nil)
		return
	}
	switch r.Method {
	case http.MethodGet:
		item, err := h.facade.GetSharedVolumeClaim(name)
		if err != nil {
			writeVolumeReclaimError(w, err)
			return
		}
		writeSuccess(w, http.StatusOK, item)
	case http.MethodDelete:
		force, err := parseBooleanFormValue(r.URL.Query().Get("force"), "force")
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
			return
		}
		if err := h.facade.RemoveSharedVolume(name, force); err != nil {
			writeVolumeReclaimError(w, err)
			return
		}
		writeSuccess(w, http.StatusOK, map[string]any{
			"status": "ok",
			"name":   name,
			"scope":  "shared",
		})
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
	}
}

func (h *EdgeletAPIHandler) HandleVolumePrune(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	req, err := parseVolumePruneRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
		return
	}
	result, err := h.facade.PruneVolumeOrphans(req)
	if err != nil {
		writeVolumeReclaimError(w, err)
		return
	}
	writeSuccess(w, http.StatusOK, result)
}

func parseVolumePruneRequest(r *http.Request) (runtimeapi.VolumePruneRequest, error) {
	req := runtimeapi.VolumePruneRequest{
		Orphans: true,
		DryRun:  true,
	}
	q := r.URL.Query()
	if raw := strings.TrimSpace(q.Get("orphans")); raw != "" {
		orphans, err := parseBooleanFormValue(raw, "orphans")
		if err != nil {
			return req, err
		}
		req.Orphans = orphans
	}
	if raw := strings.TrimSpace(q.Get("yes")); raw != "" {
		yes, err := parseBooleanFormValue(raw, "yes")
		if err != nil {
			return req, err
		}
		req.Yes = yes
	}
	if raw := strings.TrimSpace(q.Get("force")); raw != "" {
		force, err := parseBooleanFormValue(raw, "force")
		if err != nil {
			return req, err
		}
		req.Force = force
	}
	dryRunSet := strings.TrimSpace(q.Get("dryRun")) != ""
	if dryRunSet {
		dryRun, err := parseBooleanFormValue(q.Get("dryRun"), "dryRun")
		if err != nil {
			return req, err
		}
		req.DryRun = dryRun
	}
	if r.Body != nil {
		var body struct {
			Orphans *bool `json:"orphans"`
			Yes     *bool `json:"yes"`
			Force   *bool `json:"force"`
			DryRun  *bool `json:"dryRun"`
		}
		dec := json.NewDecoder(r.Body)
		if decodeErr := dec.Decode(&body); decodeErr == nil {
			if body.Orphans != nil && strings.TrimSpace(q.Get("orphans")) == "" {
				req.Orphans = *body.Orphans
			}
			if body.Yes != nil && strings.TrimSpace(q.Get("yes")) == "" {
				req.Yes = *body.Yes
			}
			if body.Force != nil && strings.TrimSpace(q.Get("force")) == "" {
				req.Force = *body.Force
			}
			if body.DryRun != nil && !dryRunSet {
				req.DryRun = *body.DryRun
			}
		} else if !errors.Is(decodeErr, io.EOF) && r.ContentLength > 0 {
			return req, errors.New("invalid JSON body")
		}
	}
	if req.Yes && !dryRunSet {
		req.DryRun = false
	}
	if !req.Orphans {
		return req, errors.New("volume prune supports only orphans")
	}
	return req, nil
}

func writeVolumeReclaimError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	switch {
	case errors.Is(err, volumereclaim.ErrNotFound):
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, err.Error(), nil)
	case errors.Is(err, volumereclaim.ErrKeepSet), errors.Is(err, volumereclaim.ErrInUse), errors.Is(err, volumereclaim.ErrControlPlane):
		writeAPIError(w, http.StatusConflict, ErrCodeConflict, err.Error(), nil)
	case errors.Is(err, volumereclaim.ErrPathJail):
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
	default:
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "not found") {
			writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, err.Error(), nil)
			return
		}
		if strings.Contains(msg, "required") || strings.Contains(msg, "is shared") {
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
			return
		}
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
	}
}
