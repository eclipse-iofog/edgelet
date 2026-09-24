package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/fieldagent"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/processmanager"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/pkg/engine"
)

type controlPlaneAPITestEngine struct {
	removeVolumeNames []string
}

func (e *controlPlaneAPITestEngine) Init(engine.EngineConfig) error { return nil }
func (e *controlPlaneAPITestEngine) Close() error                   { return nil }
func (e *controlPlaneAPITestEngine) GetContainer(string) (*engine.Container, error) {
	return nil, nil
}
func (e *controlPlaneAPITestEngine) GetContainerByID(string) (*engine.Container, error) {
	return nil, nil
}
func (e *controlPlaneAPITestEngine) GetContainerSandboxID(string) (string, error) { return "", nil }
func (e *controlPlaneAPITestEngine) GetRunningContainers() ([]engine.Container, error) {
	return nil, nil
}
func (e *controlPlaneAPITestEngine) GetAllContainers() ([]engine.Container, error) { return nil, nil }
func (e *controlPlaneAPITestEngine) CreateContainer(ms *models.Microservice, _ string) (string, error) {
	return "cp-api-" + ms.MicroserviceUUID, nil
}
func (e *controlPlaneAPITestEngine) StartContainer(string) error        { return nil }
func (e *controlPlaneAPITestEngine) StopContainer(string) error         { return nil }
func (e *controlPlaneAPITestEngine) KillContainer(string) error         { return nil }
func (e *controlPlaneAPITestEngine) RemoveContainer(string, bool) error { return nil }
func (e *controlPlaneAPITestEngine) PullImage(string, *models.Registry, *engine.PullImageOptions) error {
	return nil
}
func (e *controlPlaneAPITestEngine) FindLocalImage(string, string, bool) (bool, error) {
	return true, nil
}
func (e *controlPlaneAPITestEngine) RemoveImage(string) error { return nil }
func (e *controlPlaneAPITestEngine) PruneImages() error       { return nil }
func (e *controlPlaneAPITestEngine) ListImages(context.Context) ([]engine.ImageInfo, error) {
	return nil, nil
}
func (e *controlPlaneAPITestEngine) LoadImageFromPath(context.Context, string) ([]engine.LoadedImage, error) {
	return nil, nil
}
func (e *controlPlaneAPITestEngine) DeleteImage(context.Context, string) error { return nil }
func (e *controlPlaneAPITestEngine) PruneDangling(context.Context) (*engine.ImagePruneReport, error) {
	return nil, nil
}
func (e *controlPlaneAPITestEngine) PruneContainers(context.Context) (*engine.ContainerPruneReport, error) {
	return nil, nil
}
func (e *controlPlaneAPITestEngine) PruneVolumes(context.Context) (*engine.VolumePruneReport, error) {
	return nil, nil
}
func (e *controlPlaneAPITestEngine) RemoveNamedVolume(_ context.Context, name string) error {
	e.removeVolumeNames = append(e.removeVolumeNames, name)
	return nil
}
func (e *controlPlaneAPITestEngine) GetContainerStatus(string, string) (*models.MicroserviceStatus, error) {
	return nil, nil
}
func (e *controlPlaneAPITestEngine) GetContainerStats(string) (*engine.ContainerStats, error) {
	return nil, nil
}
func (e *controlPlaneAPITestEngine) GetContainerIPAddress(string) (string, error) { return "", nil }
func (e *controlPlaneAPITestEngine) GetContainerStartedAt(string) (int64, error)  { return 0, nil }
func (e *controlPlaneAPITestEngine) InspectContainerRaw(string) (map[string]any, error) {
	return nil, nil
}
func (e *controlPlaneAPITestEngine) TailContainerLogs(string, string, string, engine.LogTailHandler, *engine.TailConfig) error {
	return nil
}
func (e *controlPlaneAPITestEngine) AreMicroserviceAndContainerEqual(string, *models.Microservice, *models.Registry) bool {
	return false
}
func (e *controlPlaneAPITestEngine) EnsureNetwork(string) error { return nil }
func (e *controlPlaneAPITestEngine) CreateExecSession(string, string, []string) (string, error) {
	return "", nil
}
func (e *controlPlaneAPITestEngine) StartExecSession(string, io.Reader, io.Writer, io.Writer) error {
	return nil
}
func (e *controlPlaneAPITestEngine) GetExecSessionStatus(string) (bool, error)  { return false, nil }
func (e *controlPlaneAPITestEngine) GetExecSessionExitCode(string) (int, error) { return 0, nil }
func (e *controlPlaneAPITestEngine) ResizeExecSession(string, uint32, uint32) error {
	return nil
}
func (e *controlPlaneAPITestEngine) StopExecSession(string) error { return nil }
func (e *controlPlaneAPITestEngine) GetContainerMicroserviceUUID(engine.Container) string {
	return ""
}
func (e *controlPlaneAPITestEngine) GetContainerName(engine.Container) string { return "" }

var _ engine.ContainerEngine = (*controlPlaneAPITestEngine)(nil)

func setupControlPlaneAPITest(t *testing.T) (*EdgeletAPIHandler, *controlPlaneAPITestEngine) {
	t.Helper()
	ensureControlPlaneStoreDB(t)
	fieldagent.GetInstance().SetControllerStatus(models.ControllerStatusNotProvisioned)
	eng := &controlPlaneAPITestEngine{}
	processmanager.ConfigureEngineForTest(eng)
	return NewEdgeletAPIHandler(), eng
}

func ensureControlPlaneStoreDB(t *testing.T) {
	t.Helper()
	db := store.GetInstance()
	_ = db.Close()
	if err := db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open store db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
}

func decodeSuccessData(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var envelope apiSuccessEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, string(body))
	}
	if !envelope.Success {
		t.Fatalf("expected success envelope, got %s", string(body))
	}
	data, ok := envelope.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected data object, got %#v", envelope.Data)
	}
	return data
}

func pollControlPlaneApplyUntilTerminal(t *testing.T, handler *EdgeletAPIHandler, operationID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		statusReq := httptest.NewRequest(http.MethodGet, "/v1/deploy/controlplane:apply/"+operationID, nil)
		statusRec := httptest.NewRecorder()
		handler.HandleDeployControlPlaneApplyStatus(statusRec, statusReq)
		if statusRec.Code != http.StatusOK {
			t.Fatalf("status poll code=%d body=%s", statusRec.Code, statusRec.Body.String())
		}
		data := decodeSuccessData(t, statusRec.Body.Bytes())
		status, ok := data["status"].(string)
		if !ok {
			t.Fatal("type assertion failed for status")
		}
		if status == "succeeded" || status == "failed" {
			return data
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("control plane apply operation %s did not reach terminal state", operationID)
	return nil
}

func TestControlPlaneHandlers_ApplyGetManifestDelete(t *testing.T) {
	handler, eng := setupControlPlaneAPITest(t)

	applyReq := newControlPlaneManifestRequest(t, "/v1/deploy/controlplane:apply", minimalControlPlaneManifestYAMLForAPI(), nil)
	applyRec := httptest.NewRecorder()
	handler.HandleDeployControlPlaneApply(applyRec, applyReq)
	if applyRec.Code != http.StatusAccepted {
		t.Fatalf("apply status=%d body=%s", applyRec.Code, applyRec.Body.String())
	}
	applyData := decodeSuccessData(t, applyRec.Body.Bytes())
	operationID, ok := applyData["operationId"].(string)
	if !ok {
		t.Fatal("type assertion failed for operationID")
	}
	if strings.TrimSpace(operationID) == "" {
		t.Fatalf("expected operationId, got %#v", applyData)
	}
	final := pollControlPlaneApplyUntilTerminal(t, handler, operationID)
	if final["status"] != "succeeded" {
		t.Fatalf("expected succeeded, got %#v", final)
	}
	controllerUUID, ok := final["controllerUuid"].(string)
	if !ok {
		t.Fatal("type assertion failed for controllerUUID")
	}
	if strings.TrimSpace(controllerUUID) == "" {
		t.Fatal("expected controllerUuid in terminal apply status")
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/v1/system/controlplane", nil)
	statusRec := httptest.NewRecorder()
	handler.HandleSystemControlPlane(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", statusRec.Code, statusRec.Body.String())
	}
	statusData := decodeSuccessData(t, statusRec.Body.Bytes())
	if statusData["controllerUuid"] != controllerUUID {
		t.Fatalf("expected controllerUuid %q, got %#v", controllerUUID, statusData["controllerUuid"])
	}
	if statusData["runtimeState"] != "running" {
		t.Fatalf("expected runtimeState=running, got %#v", statusData["runtimeState"])
	}

	aliasReq := httptest.NewRequest(http.MethodGet, "/v1/system/controller", nil)
	aliasRec := httptest.NewRecorder()
	handler.HandleSystemControllerStatus(aliasRec, aliasReq)
	if aliasRec.Code != http.StatusOK {
		t.Fatalf("alias status=%d body=%s", aliasRec.Code, aliasRec.Body.String())
	}

	manifestReq := httptest.NewRequest(http.MethodGet, "/v1/system/controlplane/manifest", nil)
	manifestRec := httptest.NewRecorder()
	handler.HandleSystemControlPlane(manifestRec, manifestReq)
	if manifestRec.Code != http.StatusOK {
		t.Fatalf("manifest status=%d body=%s", manifestRec.Code, manifestRec.Body.String())
	}
	manifestData := decodeSuccessData(t, manifestRec.Body.Bytes())
	manifestYAML, ok := manifestData["manifestYaml"].(string)
	if !ok {
		t.Fatal("type assertion failed for manifestYAML")
	}
	if !strings.Contains(manifestYAML, "***") {
		t.Fatalf("expected masked secret marker, got %q", manifestYAML)
	}
	if strings.Contains(manifestYAML, "SuperSecret12!") {
		t.Fatal("expected bootstrap password value to be redacted")
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/v1/system/controlplane", nil)
	deleteRec := httptest.NewRecorder()
	handler.HandleSystemControlPlane(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	if len(eng.removeVolumeNames) != 0 {
		t.Fatalf("control-plane delete must not use named-volume removal, got %#v", eng.removeVolumeNames)
	}
	if _, err := store.GetInstance().GetPersistentVolume(controllerUUID, "iofog-controller-db"); err == nil {
		t.Fatal("expected control-plane db volume ledger row to be deleted")
	}
	if _, err := store.GetInstance().GetPersistentVolume(controllerUUID, "iofog-controller-log"); err == nil {
		t.Fatal("expected control-plane log volume ledger row to be deleted")
	}

	_, found, err := store.GetInstance().GetSystemControlPlane()
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if found {
		t.Fatal("expected control plane row to be deleted")
	}
}

type slowControlPlaneAPITestEngine struct {
	controlPlaneAPITestEngine
}

func (e *slowControlPlaneAPITestEngine) CreateContainer(ms *models.Microservice, hostname string) (string, error) {
	time.Sleep(400 * time.Millisecond)
	return e.controlPlaneAPITestEngine.CreateContainer(ms, hostname)
}

func TestControlPlaneHandlers_ApplyConcurrentReturns409(t *testing.T) {
	ensureControlPlaneStoreDB(t)
	processmanager.ConfigureEngineForTest(&slowControlPlaneAPITestEngine{})
	handler := NewEdgeletAPIHandler()

	firstReq := newControlPlaneManifestRequest(t, "/v1/deploy/controlplane:apply", minimalControlPlaneManifestYAMLForAPI(), nil)
	firstRec := httptest.NewRecorder()
	handler.HandleDeployControlPlaneApply(firstRec, firstReq)
	if firstRec.Code != http.StatusAccepted {
		t.Fatalf("first apply status=%d body=%s", firstRec.Code, firstRec.Body.String())
	}
	firstData := decodeSuccessData(t, firstRec.Body.Bytes())
	firstOp, ok := firstData["operationId"].(string)
	if !ok {
		t.Fatal("type assertion failed for firstOp")
	}
	if strings.TrimSpace(firstOp) == "" {
		t.Fatal("expected first operationId")
	}

	secondReq := newControlPlaneManifestRequest(t, "/v1/deploy/controlplane:apply", minimalControlPlaneManifestYAMLForAPI(), nil)
	secondRec := httptest.NewRecorder()
	handler.HandleDeployControlPlaneApply(secondRec, secondReq)
	if secondRec.Code != http.StatusConflict {
		t.Fatalf("expected 409 on concurrent apply, got %d body=%s", secondRec.Code, secondRec.Body.String())
	}
	var errEnvelope apiErrorEnvelope
	if err := json.Unmarshal(secondRec.Body.Bytes(), &errEnvelope); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if errEnvelope.Error.Code != ErrCodeApplyInProgress {
		t.Fatalf("expected %s, got %s", ErrCodeApplyInProgress, errEnvelope.Error.Code)
	}
	activeID, ok := errEnvelope.Error.Details["activeOperationId"].(string)
	if !ok {
		t.Fatal("type assertion failed for activeID")
	}
	if activeID != firstOp {
		t.Fatalf("expected activeOperationId=%q, got %q", firstOp, activeID)
	}

	_ = pollControlPlaneApplyUntilTerminal(t, handler, firstOp)
}

func TestControlPlaneHandlers_PatchRejectsIdentityChange(t *testing.T) {
	handler, _ := setupControlPlaneAPITest(t)

	applyReq := newControlPlaneManifestRequest(t, "/v1/deploy/controlplane:apply", minimalControlPlaneManifestYAMLForAPI(), nil)
	applyRec := httptest.NewRecorder()
	handler.HandleDeployControlPlaneApply(applyRec, applyReq)
	if applyRec.Code != http.StatusAccepted {
		t.Fatalf("initial apply status=%d body=%s", applyRec.Code, applyRec.Body.String())
	}
	applyData := decodeSuccessData(t, applyRec.Body.Bytes())
	opID, ok := applyData["operationId"].(string)
	if !ok {
		t.Fatal("type assertion failed for opID")
	}
	final := pollControlPlaneApplyUntilTerminal(t, handler, opID)
	if final["status"] != "succeeded" {
		t.Fatalf("initial apply failed: %#v", final)
	}

	patchManifest := strings.Replace(minimalControlPlaneManifestYAMLForAPI(), "name: pot", "name: other", 1)
	patchReq := newControlPlaneManifestRequest(t, "/v1/deploy/controlplane:apply", patchManifest, nil)
	patchRec := httptest.NewRecorder()
	handler.HandleDeployControlPlaneApply(patchRec, patchReq)
	if patchRec.Code != http.StatusAccepted {
		t.Fatalf("patch apply accepted status=%d body=%s", patchRec.Code, patchRec.Body.String())
	}
	patchData := decodeSuccessData(t, patchRec.Body.Bytes())
	patchFinal := pollControlPlaneApplyUntilTerminal(t, handler, patchData["operationId"].(string))
	if patchFinal["status"] != "failed" {
		t.Fatalf("expected failed patch terminal status, got %#v", patchFinal)
	}
}

func TestControlPlaneHandlers_ValidateDryRun(t *testing.T) {
	handler, _ := setupControlPlaneAPITest(t)

	validateReq := newControlPlaneManifestRequest(t, "/v1/deploy/controlplane:validate", minimalControlPlaneManifestYAMLForAPI(), nil)
	validateRec := httptest.NewRecorder()
	handler.HandleDeployControlPlaneValidate(validateRec, validateReq)
	if validateRec.Code != http.StatusOK {
		t.Fatalf("validate status=%d body=%s", validateRec.Code, validateRec.Body.String())
	}

	dryRunReq := newControlPlaneManifestRequest(t, "/v1/deploy/controlplane:apply", minimalControlPlaneManifestYAMLForAPI(), map[string]string{"dryRun": "true"})
	dryRunRec := httptest.NewRecorder()
	handler.HandleDeployControlPlaneApply(dryRunRec, dryRunReq)
	if dryRunRec.Code != http.StatusAccepted {
		t.Fatalf("dry-run status=%d body=%s", dryRunRec.Code, dryRunRec.Body.String())
	}
	dryData := decodeSuccessData(t, dryRunRec.Body.Bytes())
	final := pollControlPlaneApplyUntilTerminal(t, handler, dryData["operationId"].(string))
	if final["status"] != "succeeded" {
		t.Fatalf("expected dry-run succeeded, got %#v", final)
	}
	_, found, err := store.GetInstance().GetSystemControlPlane()
	if err != nil {
		t.Fatalf("get after dry-run: %v", err)
	}
	if found {
		t.Fatal("expected no persisted row after dry-run")
	}
}

func TestControlPlaneHandlers_NotFound(t *testing.T) {
	handler, _ := setupControlPlaneAPITest(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/system/controlplane", nil)
	rec := httptest.NewRecorder()
	handler.HandleSystemControlPlane(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestControlPlaneHandlers_Restart(t *testing.T) {
	handler, _ := setupControlPlaneAPITest(t)

	applyReq := newControlPlaneManifestRequest(t, "/v1/deploy/controlplane:apply", minimalControlPlaneManifestYAMLForAPI(), nil)
	applyRec := httptest.NewRecorder()
	handler.HandleDeployControlPlaneApply(applyRec, applyReq)
	if applyRec.Code != http.StatusAccepted {
		t.Fatalf("apply status=%d body=%s", applyRec.Code, applyRec.Body.String())
	}
	applyData := decodeSuccessData(t, applyRec.Body.Bytes())
	operationID, ok := applyData["operationId"].(string)
	if !ok {
		t.Fatal("type assertion failed for operationID")
	}
	final := pollControlPlaneApplyUntilTerminal(t, handler, operationID)
	if final["status"] != "succeeded" {
		t.Fatalf("expected succeeded apply, got %#v", final)
	}
	controllerUUID, ok := final["controllerUuid"].(string)
	if !ok || strings.TrimSpace(controllerUUID) == "" {
		t.Fatalf("expected controllerUuid, got %#v", final["controllerUuid"])
	}

	restartReq := httptest.NewRequest(http.MethodPost, "/v1/system/controlplane/restart", nil)
	restartRec := httptest.NewRecorder()
	handler.HandleSystemControlPlaneRestart(restartRec, restartReq)
	if restartRec.Code != http.StatusOK {
		t.Fatalf("restart status=%d body=%s", restartRec.Code, restartRec.Body.String())
	}
	restartData := decodeSuccessData(t, restartRec.Body.Bytes())
	if restartData["status"] != "ok" {
		t.Fatalf("expected status ok, got %#v", restartData["status"])
	}
	if restartData["controllerUuid"] != controllerUUID {
		t.Fatalf("expected controllerUuid %q, got %#v", controllerUUID, restartData["controllerUuid"])
	}
	if restartData["runtimeState"] != "running" {
		t.Fatalf("expected runtimeState=running, got %#v", restartData["runtimeState"])
	}
}

func TestControlPlaneHandlers_RestartMethodNotAllowed(t *testing.T) {
	handler, _ := setupControlPlaneAPITest(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/system/controlplane/restart", nil)
	rec := httptest.NewRecorder()
	handler.HandleSystemControlPlaneRestart(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestControlPlaneHandlers_RestartNotFound(t *testing.T) {
	handler, _ := setupControlPlaneAPITest(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/system/controlplane/restart", nil)
	rec := httptest.NewRecorder()
	handler.HandleSystemControlPlaneRestart(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func newControlPlaneManifestRequest(t *testing.T, path, manifest string, fields map[string]string) *http.Request {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("manifest", "controlplane.yaml")
	if err != nil {
		t.Fatalf("create manifest part: %v", err)
	}
	if _, err := part.Write([]byte(manifest)); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field %s: %v", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func minimalControlPlaneManifestYAMLForAPI() string {
	return `apiVersion: edgelet.iofog.org/v1
kind: ControlPlane
metadata:
  name: pot
  namespace: default
spec:
  controller:
    image: ghcr.io/datasance/controller:3.8.0-beta.0
  auth:
    mode: embedded
    bootstrap:
      username: admin
      password: SuperSecret12!
`
}
