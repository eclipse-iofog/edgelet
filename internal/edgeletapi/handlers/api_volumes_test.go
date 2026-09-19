package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/runtimeapi"
	"github.com/eclipse-iofog/edgelet/internal/store"
)

func setupVolumeAPI(t *testing.T) (string, *store.DB, *EdgeletAPIHandler) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.GetInstance()
	cfg.DiskDirectory = dir
	db := store.GetInstance()
	if conn := db.Conn(); conn != nil {
		_ = db.Close()
	}
	if err := db.Open(dir); err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return dir, db, NewEdgeletAPIHandler()
}

func writeVolumeMarker(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
}

func requireMarker(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected marker to remain at %s: %v", path, err)
	}
}

func decodeVolumeData(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
		Error   map[string]any `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
	}
	if !envelope.Success {
		t.Fatalf("expected success, got error=%s %s", fmtString(envelope.Error["code"]), fmtString(envelope.Error["message"]))
	}
	return envelope.Data
}

func asSlice(v any) []any {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	return raw
}

func asMap(v any) map[string]any {
	raw, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return raw
}

func asBool(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

func TestHandleVolumes_ListIncludesScopeAndMixedConsumers(t *testing.T) {
	dir, db, handler := setupVolumeAPI(t)
	privateUUID := "ms-private"
	localUUID := "local-uuid"
	controllerUUID := "controller-uuid"
	privatePath := filepath.Join(dir, "volumes", "data", privateUUID, "data")
	sharedPath := filepath.Join(dir, "volumes", "shared", "shared-config")
	writeVolumeMarker(t, filepath.Join(privatePath, "keep.txt"))
	writeVolumeMarker(t, filepath.Join(sharedPath, "keep.txt"))

	if err := db.UpsertPersistentVolume(privateUUID, "data", store.PersistentVolumeKindWorkload, models.VolumeScopePrivate, privatePath); err != nil {
		t.Fatalf("upsert private: %v", err)
	}
	if err := db.UpsertPersistentVolume(localUUID, "shared-config", store.PersistentVolumeKindWorkload, models.VolumeScopeShared, sharedPath); err != nil {
		t.Fatalf("upsert local shared: %v", err)
	}
	if err := db.UpsertPersistentVolume(controllerUUID, "shared-config", store.PersistentVolumeKindWorkload, models.VolumeScopeShared, sharedPath); err != nil {
		t.Fatalf("upsert controller shared: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/volumes", nil)
	rec := httptest.NewRecorder()
	handler.HandleVolumes(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	data := decodeVolumeData(t, rec)
	raw := asSlice(data["volumes"])
	if len(raw) < 2 {
		t.Fatalf("expected private and shared claims, got %#v", data)
	}
	var sawPrivate, sawShared bool
	for _, item := range raw {
		claim := asMap(item)
		if claim == nil {
			t.Fatalf("expected claim object, got %#v", item)
		}
		scope := fmtString(claim["scope"])
		if scope == models.VolumeScopePrivate {
			sawPrivate = true
			if fmtString(claim["uuid"]) != privateUUID {
				t.Fatalf("private uuid: %#v", claim)
			}
		}
		if scope == models.VolumeScopeShared && fmtString(claim["name"]) == "shared-config" {
			sawShared = true
			if claim["uuid"] != nil {
				t.Fatalf("shared uuid must be null, got %#v", claim["uuid"])
			}
			consumers := asSlice(claim["consumers"])
			got := map[string]bool{}
			for _, c := range consumers {
				got[fmtString(c)] = true
			}
			if !got[localUUID] || !got[controllerUUID] {
				t.Fatalf("expected local and controller consumers, got %#v", consumers)
			}
		}
	}
	if !sawPrivate || !sawShared {
		t.Fatalf("missing claims: private=%v shared=%v payload=%#v", sawPrivate, sawShared, data)
	}
}

func TestHandleVolumePrune_DryRunDoesNotDelete(t *testing.T) {
	dir, db, handler := setupVolumeAPI(t)
	uuid := "ms-orphan"
	marker := filepath.Join(dir, "volumes", "data", uuid, "data", "keep.txt")
	writeVolumeMarker(t, marker)
	if err := db.UpsertPersistentVolume(uuid, "data", store.PersistentVolumeKindWorkload, models.VolumeScopePrivate, filepath.Dir(marker)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := db.MarkPersistentVolumesUnreferenced(uuid); err != nil {
		t.Fatalf("unreference: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/volumes:prune", nil)
	rec := httptest.NewRecorder()
	handler.HandleVolumePrune(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	data := decodeVolumeData(t, rec)
	if !asBool(data["dryRun"]) {
		t.Fatalf("expected dry-run, got %#v", data)
	}
	requireMarker(t, marker)
}

func TestHandleVolumes_DeletePrivateWhileDesiredConflicts(t *testing.T) {
	dir, db, handler := setupVolumeAPI(t)
	uuid := "ms-desired"
	marker := filepath.Join(dir, "volumes", "data", uuid, "data", "keep.txt")
	writeVolumeMarker(t, marker)
	hostPath := filepath.Dir(marker)
	if err := db.UpsertLocalWorkload(&models.LocalDeployedMicroservice{
		LocalUUID:        uuid,
		ApplicationName:  "edgelet",
		MicroserviceName: "desired",
		SourceName:       "local-cli",
		ManifestYAML:     "kind: Microservice",
		ImageName:        "nginx:latest",
		DesiredState:     "running",
		RuntimeState:     "running",
		State:            "running",
	}); err != nil {
		t.Fatalf("upsert workload: %v", err)
	}
	if err := db.UpsertPersistentVolume(uuid, "data", store.PersistentVolumeKindWorkload, models.VolumeScopePrivate, hostPath); err != nil {
		t.Fatalf("upsert volume: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/v1/volumes/"+uuid, nil)
	rec := httptest.NewRecorder()
	handler.HandleVolumes(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%s", rec.Code, rec.Body.String())
	}
	requireMarker(t, marker)
}

func TestHandleVolumes_DeletePrivateForceRemovesUnmounted(t *testing.T) {
	dir, db, handler := setupVolumeAPI(t)
	uuid := "ms-force"
	marker := filepath.Join(dir, "volumes", "data", uuid, "data", "keep.txt")
	writeVolumeMarker(t, marker)
	if err := db.UpsertPersistentVolume(uuid, "data", store.PersistentVolumeKindWorkload, models.VolumeScopePrivate, filepath.Dir(marker)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/v1/volumes/"+uuid+"?force=true", nil)
	rec := httptest.NewRecorder()
	handler.HandleVolumes(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("expected private dir removed, stat=%v", err)
	}
}

func TestHandleVolumeShared_DeleteWithRemainingConsumersConflicts(t *testing.T) {
	dir, db, handler := setupVolumeAPI(t)
	name := "shared-config"
	sharedPath := filepath.Join(dir, "volumes", "shared", name)
	marker := filepath.Join(sharedPath, "keep.txt")
	writeVolumeMarker(t, marker)
	if err := db.UpsertLocalWorkload(&models.LocalDeployedMicroservice{
		LocalUUID:        "still-here",
		ApplicationName:  "edgelet",
		MicroserviceName: "live",
		SourceName:       "local-cli",
		ManifestYAML:     "kind: Microservice",
		ImageName:        "nginx:latest",
		DesiredState:     "running",
		RuntimeState:     "running",
		State:            "running",
	}); err != nil {
		t.Fatalf("upsert workload: %v", err)
	}
	if err := db.UpsertPersistentVolume("still-here", name, store.PersistentVolumeKindWorkload, models.VolumeScopeShared, sharedPath); err != nil {
		t.Fatalf("upsert live: %v", err)
	}
	if err := db.UpsertPersistentVolume("gone", name, store.PersistentVolumeKindWorkload, models.VolumeScopeShared, sharedPath); err != nil {
		t.Fatalf("upsert gone: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/v1/volumes/shared/"+name, nil)
	rec := httptest.NewRecorder()
	handler.HandleVolumeShared(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%s", rec.Code, rec.Body.String())
	}
	requireMarker(t, marker)
}

func TestHandleVolumes_PrivateDeleteDoesNotRemoveSharedDisk(t *testing.T) {
	dir, db, handler := setupVolumeAPI(t)
	uuid := "ms-private-only"
	privateMarker := filepath.Join(dir, "volumes", "data", uuid, "data", "keep.txt")
	sharedMarker := filepath.Join(dir, "volumes", "shared", "shared-config", "keep.txt")
	writeVolumeMarker(t, privateMarker)
	writeVolumeMarker(t, sharedMarker)
	if err := db.UpsertPersistentVolume(uuid, "data", store.PersistentVolumeKindWorkload, models.VolumeScopePrivate, filepath.Dir(privateMarker)); err != nil {
		t.Fatalf("upsert private: %v", err)
	}
	if err := db.UpsertPersistentVolume("other", "shared-config", store.PersistentVolumeKindWorkload, models.VolumeScopeShared, filepath.Dir(sharedMarker)); err != nil {
		t.Fatalf("upsert shared: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/v1/volumes/"+uuid+"?force=true", nil)
	rec := httptest.NewRecorder()
	handler.HandleVolumes(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(privateMarker); !os.IsNotExist(err) {
		t.Fatalf("expected private marker removed, stat=%v", err)
	}
	requireMarker(t, sharedMarker)
}

func TestHandleSystemPrune_VolumesDoesNotRemovePersistentTrees(t *testing.T) {
	dir, _, handler := setupVolumeAPI(t)
	dataMarker := filepath.Join(dir, "volumes", "data", "ms-1", "data", "keep.txt")
	sharedMarker := filepath.Join(dir, "volumes", "shared", "n", "keep.txt")
	writeVolumeMarker(t, dataMarker)
	writeVolumeMarker(t, sharedMarker)

	req := httptest.NewRequest(http.MethodPost, "/v1/system/prune?mode=volumes", nil)
	rec := httptest.NewRecorder()
	handler.HandleSystemPrune(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "volume prune") {
		t.Fatalf("expected pointer at volume prune, got %s", rec.Body.String())
	}
	requireMarker(t, dataMarker)
	requireMarker(t, sharedMarker)
}

func TestHandleSystemPrune_AllDoesNotRemovePersistentTrees(t *testing.T) {
	dir, _, handler := setupVolumeAPI(t)
	dataMarker := filepath.Join(dir, "volumes", "data", "ms-1", "data", "keep.txt")
	sharedMarker := filepath.Join(dir, "volumes", "shared", "n", "keep.txt")
	writeVolumeMarker(t, dataMarker)
	writeVolumeMarker(t, sharedMarker)

	req := httptest.NewRequest(http.MethodPost, "/v1/system/prune?mode=all", nil)
	rec := httptest.NewRecorder()
	handler.HandleSystemPrune(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected status %d body=%s", rec.Code, rec.Body.String())
	}
	requireMarker(t, dataMarker)
	requireMarker(t, sharedMarker)
}

func TestHandleMicroservices_RemoveRetainsPrivateVolume(t *testing.T) {
	dir, db, handler := setupVolumeAPI(t)
	uuid := "ms-rm-keep"
	marker := filepath.Join(dir, "volumes", "data", uuid, "data", "keep.txt")
	writeVolumeMarker(t, marker)
	if err := db.UpsertLocalWorkload(&models.LocalDeployedMicroservice{
		LocalUUID:        uuid,
		ApplicationName:  "edgelet",
		MicroserviceName: "keep",
		SourceName:       "local-cli",
		ManifestYAML:     "kind: Microservice",
		ImageName:        "nginx:latest",
		DesiredState:     "running",
		RuntimeState:     "running",
		State:            "running",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := db.UpsertPersistentVolume(uuid, "data", store.PersistentVolumeKindWorkload, models.VolumeScopePrivate, filepath.Dir(marker)); err != nil {
		t.Fatalf("upsert volume: %v", err)
	}

	before := runtime.NumGoroutine()
	req := httptest.NewRequest(http.MethodDelete, "/v1/ms/"+uuid, nil)
	rec := httptest.NewRecorder()
	handler.HandleMicroservices(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	requireMarker(t, marker)
	if rec := mustGetVolume(t, db, uuid, "data"); rec.UnreferencedAt == nil {
		t.Fatal("expected unreferenced_at after ms rm")
	}
	after := runtime.NumGoroutine()
	if after > before+5 {
		t.Fatalf("ms rm must not start a sweeper, goroutines before=%d after=%d", before, after)
	}
}

func TestHandleMicroservices_RemoveCleanupReservedDoesNotDelete(t *testing.T) {
	dir, db, handler := setupVolumeAPI(t)
	uuid := "ms-rm-cleanup"
	marker := filepath.Join(dir, "volumes", "data", uuid, "data", "keep.txt")
	writeVolumeMarker(t, marker)
	if err := db.UpsertLocalWorkload(&models.LocalDeployedMicroservice{
		LocalUUID:        uuid,
		ApplicationName:  "edgelet",
		MicroserviceName: "cleanup",
		SourceName:       "local-cli",
		ManifestYAML:     "kind: Microservice",
		ImageName:        "nginx:latest",
		DesiredState:     "running",
		RuntimeState:     "running",
		State:            "running",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := db.UpsertPersistentVolume(uuid, "data", store.PersistentVolumeKindWorkload, models.VolumeScopePrivate, filepath.Dir(marker)); err != nil {
		t.Fatalf("upsert volume: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/v1/ms/"+uuid+"?cleanup=true", nil)
	rec := httptest.NewRecorder()
	handler.HandleMicroservices(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	requireMarker(t, marker)
	uuids, err := db.ListPersistentVolumeCleanupUUIDs()
	if err != nil {
		t.Fatalf("list cleanup: %v", err)
	}
	found := false
	for _, item := range uuids {
		if item == uuid {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected cleanup bit for %s, got %v", uuid, uuids)
	}
}

func TestHandleVolumeShared_MissingNameNotFound(t *testing.T) {
	_, _, handler := setupVolumeAPI(t)
	req := httptest.NewRequest(http.MethodDelete, "/v1/volumes/shared/missing-name", nil)
	rec := httptest.NewRecorder()
	handler.HandleVolumeShared(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleVolumes_SharedTokenIsNotAPrivateUUID(t *testing.T) {
	dir, db, handler := setupVolumeAPI(t)
	sharedMarker := filepath.Join(dir, "volumes", "shared", "shared-config", "keep.txt")
	writeVolumeMarker(t, sharedMarker)
	if err := db.UpsertPersistentVolume("other", "shared-config", store.PersistentVolumeKindWorkload, models.VolumeScopeShared, filepath.Dir(sharedMarker)); err != nil {
		t.Fatalf("upsert shared: %v", err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/v1/volumes/shared?force=true", nil)
	rec := httptest.NewRecorder()
	handler.HandleVolumes(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for private shared token, got %d body=%s", rec.Code, rec.Body.String())
	}
	requireMarker(t, sharedMarker)
}

func TestFacadePrune_VolumesModeRefusesPersistentData(t *testing.T) {
	dir, _, _ := setupVolumeAPI(t)
	marker := filepath.Join(dir, "volumes", "data", "ms-1", "data", "keep.txt")
	writeVolumeMarker(t, marker)
	f := runtimeapi.NewFacade()
	_, err := f.Prune("volumes")
	if err == nil || !strings.Contains(err.Error(), "volume prune") {
		t.Fatalf("expected volumes mode error, got %v", err)
	}
	requireMarker(t, marker)
}

func fmtString(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprintf("%v", v))
}

func mustGetVolume(t *testing.T, db *store.DB, uuid, name string) *store.PersistentVolumeRecord {
	t.Helper()
	rec, err := db.GetPersistentVolume(uuid, name)
	if err != nil {
		t.Fatalf("get volume: %v", err)
	}
	return rec
}
