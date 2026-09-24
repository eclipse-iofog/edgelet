//revive:disable:nested-structs
package handlers

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/buildmeta"
	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/runtimeapi"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/internal/utils"
)

type runtimeClassDetailedTestError struct {
	msg     string
	details map[string]any
}

func (e *runtimeClassDetailedTestError) Error() string {
	return e.msg
}

func (e *runtimeClassDetailedTestError) Details() map[string]any {
	return e.details
}

func TestHandleSystemControllerCert_DecodesBase64WritesPathEnablesSecureMode(t *testing.T) {
	cfg := setupConfigForGPSTests(t)
	certPath := filepath.Join(t.TempDir(), "controller-ca.crt")
	errorsMap := cfg.SetConfig(map[string]any{"ac": certPath})
	if len(errorsMap) > 0 {
		t.Fatalf("failed to set certificate path: %v", errorsMap)
	}
	reloadCalled := false
	cfg.SetReloadCallback(func() error {
		reloadCalled = true
		return nil
	})

	handler := NewEdgeletAPIHandler()
	pemCert := generateTestCertPEM(t)
	base64Cert := base64.StdEncoding.EncodeToString([]byte(pemCert))
	reqBody := []byte(`{"certificate":` + strconv.Quote(base64Cert) + `}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/system/controller/cert", bytes.NewBuffer(reqBody))
	rec := httptest.NewRecorder()

	handler.HandleSystemControllerCert(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !reloadCalled {
		t.Fatal("expected reload callback to be called")
	}
	fileData, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("expected certificate file to be written: %v", err)
	}
	if strings.TrimSpace(string(fileData)) != strings.TrimSpace(pemCert) {
		t.Fatal("certificate file does not match decoded PEM content")
	}
	if !cfg.SecureMode {
		t.Fatal("expected secure mode to be enabled")
	}
}

func TestHandleSystemControllerCert_RejectsNonBase64Input(t *testing.T) {
	cfg := setupConfigForGPSTests(t)
	certPath := filepath.Join(t.TempDir(), "controller-ca.crt")
	errorsMap := cfg.SetConfig(map[string]any{"ac": certPath})
	if len(errorsMap) > 0 {
		t.Fatalf("failed to set certificate path: %v", errorsMap)
	}
	handler := NewEdgeletAPIHandler()
	req := httptest.NewRequest(http.MethodPost, "/v1/system/controller/cert", bytes.NewBufferString(`{"certificate":"-----BEGIN CERTIFICATE-----bad"}`))
	rec := httptest.NewRecorder()

	handler.HandleSystemControllerCert(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleSystemConfigSwitch_SwitchesProfileAndTriggersReload(t *testing.T) {
	cfg := setupConfigForGPSTests(t)
	cfg.SetCurrentProfile(utils.ConfigSwitcherStateDefault)
	reloadCalled := false
	cfg.SetReloadCallback(func() error {
		reloadCalled = true
		return nil
	})

	handler := NewEdgeletAPIHandler()
	req := httptest.NewRequest(http.MethodPost, "/v1/system/config/switch", bytes.NewBufferString(`{"profile":"dev"}`))
	rec := httptest.NewRecorder()
	handler.HandleSystemConfigSwitch(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := config.GetInstance().GetCurrentProfile().FullValue(); got != "development" {
		t.Fatalf("expected switched profile development, got %q", got)
	}
	if !reloadCalled {
		t.Fatal("expected reload callback to be called")
	}
}

func TestHandleConfig_RejectsInvalidNetworkInterfaceWithoutPersisting(t *testing.T) {
	cfg := setupConfigForGPSTests(t)
	cfg.ControllerURL = "http://127.0.0.1:51121"
	cfg.NetworkInterface = "dynamic"

	handler := NewEdgeletAPIHandler()
	req := httptest.NewRequest(http.MethodPatch, "/v1/system/config", bytes.NewBufferString(`{"set":{"networkInterface":"iface-does-not-exist-98765"}}`))
	rec := httptest.NewRecorder()
	handler.HandleConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Success bool `json:"success"`
		Error   struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if envelope.Success {
		t.Fatalf("expected success=false for invalid network interface, body=%s", rec.Body.String())
	}
	if envelope.Error.Code != ErrCodeInvalidArgument {
		t.Fatalf("expected error code %s, got %s", ErrCodeInvalidArgument, envelope.Error.Code)
	}
	if cfg.NetworkInterface != "dynamic" {
		t.Fatalf("expected networkInterface to remain unchanged, got %q", cfg.NetworkInterface)
	}
}

func TestHandleSystemProvisionDelete_RejectsInvalidScope(t *testing.T) {
	handler := NewEdgeletAPIHandler()
	req := httptest.NewRequest(http.MethodDelete, "/v1/system/provision?scope=bad", nil)
	rec := httptest.NewRecorder()

	handler.HandleSystemProvision(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleSystemProvisionDelete_RejectsInvalidPurgeVolumes(t *testing.T) {
	handler := NewEdgeletAPIHandler()
	req := httptest.NewRequest(http.MethodDelete, "/v1/system/provision?purgeVolumes=maybe", nil)
	rec := httptest.NewRecorder()

	handler.HandleSystemProvision(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleSystemPrune_RejectsInvalidMode(t *testing.T) {
	handler := NewEdgeletAPIHandler()
	req := httptest.NewRequest(http.MethodPost, "/v1/system/prune?mode=bad", nil)
	rec := httptest.NewRecorder()

	handler.HandleSystemPrune(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleSystemLogs_RejectsInvalidTail(t *testing.T) {
	cfg := setupConfigForGPSTests(t)
	cfg.LogDirectory = t.TempDir() + string(os.PathSeparator)

	handler := NewEdgeletAPIHandler()
	req := httptest.NewRequest(http.MethodGet, "/v1/system/logs?tailLines=bad", nil)
	rec := httptest.NewRecorder()

	handler.HandleSystemLogs(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleSystemLogs_BoundedReturnsEntries(t *testing.T) {
	cfg := setupConfigForGPSTests(t)
	cfg.LogDirectory = t.TempDir() + string(os.PathSeparator)
	logFile := filepath.Join(cfg.LogDirectory, "edgelet.0.log")
	logLines := strings.Join([]string{
		"2026-05-17 00:00:01.000 [info] boot",
		"2026-05-17 00:00:02.000 [info] ready",
	}, "\n") + "\n"
	if err := os.WriteFile(logFile, []byte(logLines), 0o600); err != nil {
		t.Fatalf("failed to write log file: %v", err)
	}

	handler := NewEdgeletAPIHandler()
	req := httptest.NewRequest(http.MethodGet, "/v1/system/logs?tailLines=2", nil)
	rec := httptest.NewRecorder()

	handler.HandleSystemLogs(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Success bool `json:"success"`
		Data    struct {
			Entries []map[string]any `json:"entries"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("failed to parse response: %v body=%s", err, rec.Body.String())
	}
	if !envelope.Success {
		t.Fatalf("expected success=true body=%s", rec.Body.String())
	}
	if len(envelope.Data.Entries) < 2 {
		t.Fatalf("expected at least 2 log entries, got %d body=%s", len(envelope.Data.Entries), rec.Body.String())
	}
}

func TestHandleDeployRegistries_GetByID_IncludesPassword(t *testing.T) {
	db := store.GetInstance()
	_ = db.Close()
	if err := db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	if err := db.EnsureDefaultLocalRegistries(); err != nil {
		t.Fatalf("failed to seed default registries: %v", err)
	}
	if err := db.UpsertLocalRegistry(models.NewRegistry(7, "private.example.com", false, "john", "s3cr3t", "john@example.com")); err != nil {
		t.Fatalf("failed to upsert local registry: %v", err)
	}

	handler := NewEdgeletAPIHandler()
	req := httptest.NewRequest(http.MethodGet, "/v1/deploy/registries/7", nil)
	rec := httptest.NewRecorder()

	handler.HandleDeployRegistries(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Success bool `json:"success"`
		Data    struct {
			ID       int    `json:"id"`
			Password string `json:"password"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("failed to parse response: %v body=%s", err, rec.Body.String())
	}
	if !envelope.Success {
		t.Fatalf("expected success=true body=%s", rec.Body.String())
	}
	if envelope.Data.ID != 7 {
		t.Fatalf("expected id=7, got %d", envelope.Data.ID)
	}
	if envelope.Data.Password != "s3cr3t" {
		t.Fatalf("expected password in data payload, got %q", envelope.Data.Password)
	}
}

func TestHandleDeployRuntimeClassesValidate_RejectsUnsupportedEngineOrFlavor(t *testing.T) {
	cfg := setupConfigForGPSTests(t)
	cfg.ContainerEngine = "docker"

	embedded := false
	buildmeta.SetHasEmbeddedEngineForTest(&embedded)
	t.Cleanup(func() { buildmeta.SetHasEmbeddedEngineForTest(nil) })

	handler := NewEdgeletAPIHandler()
	req := newManifestMultipartRequest(t, "/v1/deploy/runtimeclasses:validate", `
apiVersion: edgelet.iofog.org/v1
kind: RuntimeClass
metadata:
  name: edgelet-wasmtime
handler: edgelet-wasmtime
`, nil)
	rec := httptest.NewRecorder()

	handler.HandleDeployRuntimeClassesValidate(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Success bool `json:"success"`
		Error   struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if envelope.Success {
		t.Fatalf("expected success=false body=%s", rec.Body.String())
	}
	if envelope.Error.Code != ErrCodeInvalidArgument {
		t.Fatalf("expected error code %s, got %s", ErrCodeInvalidArgument, envelope.Error.Code)
	}
	expected := "runtimeclass is supported only when containerEngine=edgelet"
	if envelope.Error.Message != expected {
		t.Fatalf("unexpected gate error message: got=%q want=%q", envelope.Error.Message, expected)
	}
}

func TestHandleDeployRuntimeClassesCRUD_SucceedsWhenFullAndIofog(t *testing.T) {
	cfg := setupConfigForGPSTests(t)
	cfg.ContainerEngine = "edgelet"

	embedded := true
	buildmeta.SetHasEmbeddedEngineForTest(&embedded)
	t.Cleanup(func() { buildmeta.SetHasEmbeddedEngineForTest(nil) })

	db := store.GetInstance()
	_ = db.Close()
	if err := db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	manifest := `
apiVersion: edgelet.iofog.org/v1
kind: RuntimeClass
metadata:
  name: edgelet-wasmtime
handler: edgelet-wasmtime
`

	handler := NewEdgeletAPIHandler()

	validateReq := newManifestMultipartRequest(t, "/v1/deploy/runtimeclasses:validate", manifest, nil)
	validateRec := httptest.NewRecorder()
	handler.HandleDeployRuntimeClassesValidate(validateRec, validateReq)
	if validateRec.Code != http.StatusOK {
		t.Fatalf("expected validate 200, got %d body=%s", validateRec.Code, validateRec.Body.String())
	}

	applyReq := newManifestMultipartRequest(t, "/v1/deploy/runtimeclasses:apply", manifest, map[string]string{"dryRun": "false"})
	applyRec := httptest.NewRecorder()
	handler.HandleDeployRuntimeClassesApply(applyRec, applyReq)
	if applyRec.Code != http.StatusOK {
		t.Fatalf("expected apply 200, got %d body=%s", applyRec.Code, applyRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/v1/deploy/runtimeclasses", nil)
	listRec := httptest.NewRecorder()
	handler.HandleDeployRuntimeClasses(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected list 200, got %d body=%s", listRec.Code, listRec.Body.String())
	}
	var listEnvelope struct {
		Success bool `json:"success"`
		Data    struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listEnvelope); err != nil {
		t.Fatalf("failed to decode runtimeclass list response: %v body=%s", err, listRec.Body.String())
	}
	if !listEnvelope.Success {
		t.Fatalf("expected list success=true body=%s", listRec.Body.String())
	}
	if len(listEnvelope.Data.Items) != 1 {
		t.Fatalf("expected exactly one runtimeclass item, got=%d body=%s", len(listEnvelope.Data.Items), listRec.Body.String())
	}
	item := listEnvelope.Data.Items[0]
	if got := strings.TrimSpace(fmt.Sprintf("%v", item["name"])); got != "edgelet-wasmtime" {
		t.Fatalf("expected runtimeclass list camelCase key name=edgelet-wasmtime, got=%q body=%s", got, listRec.Body.String())
	}
	if got := strings.TrimSpace(fmt.Sprintf("%v", item["runtimeName"])); got != "edgelet-wasmtime" {
		t.Fatalf("expected runtimeclass list camelCase key runtimeName=edgelet-wasmtime, got=%q body=%s", got, listRec.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v1/deploy/runtimeclasses/edgelet-wasmtime", nil)
	getRec := httptest.NewRecorder()
	handler.HandleDeployRuntimeClasses(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("expected get 200, got %d body=%s", getRec.Code, getRec.Body.String())
	}
	var getEnvelope struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &getEnvelope); err != nil {
		t.Fatalf("failed to decode runtimeclass get response: %v body=%s", err, getRec.Body.String())
	}
	if !getEnvelope.Success {
		t.Fatalf("expected get success=true body=%s", getRec.Body.String())
	}
	if got := strings.TrimSpace(fmt.Sprintf("%v", getEnvelope.Data["name"])); got != "edgelet-wasmtime" {
		t.Fatalf("expected runtimeclass inspect camelCase key name=edgelet-wasmtime, got=%q body=%s", got, getRec.Body.String())
	}
	if got := strings.TrimSpace(fmt.Sprintf("%v", getEnvelope.Data["runtimeName"])); got != "edgelet-wasmtime" {
		t.Fatalf("expected runtimeclass inspect camelCase key runtimeName=edgelet-wasmtime, got=%q body=%s", got, getRec.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/v1/deploy/runtimeclasses/edgelet-wasmtime", nil)
	deleteRec := httptest.NewRecorder()
	handler.HandleDeployRuntimeClasses(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("expected delete 200, got %d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
}

func TestHandleDeployRuntimeClassesApply_AsyncAcceptedAndPollSucceeded(t *testing.T) {
	cfg := setupConfigForGPSTests(t)
	cfg.ContainerEngine = "edgelet"
	embedded := true
	buildmeta.SetHasEmbeddedEngineForTest(&embedded)
	t.Cleanup(func() { buildmeta.SetHasEmbeddedEngineForTest(nil) })

	manifest := `
apiVersion: edgelet.iofog.org/v1
kind: RuntimeClass
metadata:
  name: spin
handler: spin
`

	originalRunner := runtimeClassApplyRunner
	runtimeClassApplyRunner = func(_ *runtimeapi.Facade, _ string, _ bool) (*models.LocalRuntimeClass, error) {
		time.Sleep(20 * time.Millisecond)
		return &models.LocalRuntimeClass{
			Name:        "spin",
			Handler:     "spin",
			RuntimeName: "spin",
		}, nil
	}
	t.Cleanup(func() { runtimeClassApplyRunner = originalRunner })

	handler := NewEdgeletAPIHandler()
	applyReq := newManifestMultipartRequest(t, "/v1/deploy/runtimeclasses:apply", manifest, map[string]string{"async": "true"})
	applyRec := httptest.NewRecorder()
	handler.HandleDeployRuntimeClassesApply(applyRec, applyReq)
	if applyRec.Code != http.StatusAccepted {
		t.Fatalf("expected async apply 202, got %d body=%s", applyRec.Code, applyRec.Body.String())
	}
	var applyEnvelope struct {
		Success bool `json:"success"`
		Data    struct {
			OperationID string `json:"operationId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(applyRec.Body.Bytes(), &applyEnvelope); err != nil {
		t.Fatalf("failed to parse async apply response: %v body=%s", err, applyRec.Body.String())
	}
	if strings.TrimSpace(applyEnvelope.Data.OperationID) == "" {
		t.Fatalf("expected operationId in async apply response body=%s", applyRec.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		statusReq := httptest.NewRequest(http.MethodGet, "/v1/deploy/runtimeclasses:apply/"+applyEnvelope.Data.OperationID, nil)
		statusRec := httptest.NewRecorder()
		handler.HandleDeployRuntimeClassesApplyStatus(statusRec, statusReq)
		if statusRec.Code != http.StatusOK {
			t.Fatalf("expected status poll 200, got %d body=%s", statusRec.Code, statusRec.Body.String())
		}
		var statusEnvelope struct {
			Success bool `json:"success"`
			Data    struct {
				Status string `json:"status"`
				Stage  string `json:"stage"`
			} `json:"data"`
		}
		if err := json.Unmarshal(statusRec.Body.Bytes(), &statusEnvelope); err != nil {
			t.Fatalf("failed to parse poll response: %v body=%s", err, statusRec.Body.String())
		}
		if statusEnvelope.Data.Status == "succeeded" {
			if statusEnvelope.Data.Stage != runtimeapi.RuntimeClassStageDone {
				t.Fatalf("expected succeeded stage %s, got=%s body=%s", runtimeapi.RuntimeClassStageDone, statusEnvelope.Data.Stage, statusRec.Body.String())
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("operation did not reach succeeded state in time, last body=%s", statusRec.Body.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHandleDeployRuntimeClassesApply_SyncTimeoutReturnsAccepted(t *testing.T) {
	cfg := setupConfigForGPSTests(t)
	cfg.ContainerEngine = "edgelet"
	embedded := true
	buildmeta.SetHasEmbeddedEngineForTest(&embedded)
	t.Cleanup(func() { buildmeta.SetHasEmbeddedEngineForTest(nil) })

	manifest := `
apiVersion: edgelet.iofog.org/v1
kind: RuntimeClass
metadata:
  name: spin
handler: spin
`

	originalRunner := runtimeClassApplyRunner
	originalTimeout := runtimeClassApplySyncWaitTimeout
	runtimeClassApplyRunner = func(_ *runtimeapi.Facade, _ string, _ bool) (*models.LocalRuntimeClass, error) {
		time.Sleep(80 * time.Millisecond)
		return &models.LocalRuntimeClass{
			Name:        "spin",
			Handler:     "spin",
			RuntimeName: "spin",
		}, nil
	}
	runtimeClassApplySyncWaitTimeout = 5 * time.Millisecond
	t.Cleanup(func() {
		runtimeClassApplyRunner = originalRunner
		runtimeClassApplySyncWaitTimeout = originalTimeout
	})

	handler := NewEdgeletAPIHandler()
	applyReq := newManifestMultipartRequest(t, "/v1/deploy/runtimeclasses:apply", manifest, nil)
	applyRec := httptest.NewRecorder()
	handler.HandleDeployRuntimeClassesApply(applyRec, applyReq)
	if applyRec.Code != http.StatusAccepted {
		t.Fatalf("expected sync timeout fallback 202, got %d body=%s", applyRec.Code, applyRec.Body.String())
	}
}

func TestHandleDeployRuntimeClassesApply_PollFailureReturns200WithFailedData(t *testing.T) {
	cfg := setupConfigForGPSTests(t)
	cfg.ContainerEngine = "edgelet"
	embedded := true
	buildmeta.SetHasEmbeddedEngineForTest(&embedded)
	t.Cleanup(func() { buildmeta.SetHasEmbeddedEngineForTest(nil) })

	manifest := `
apiVersion: edgelet.iofog.org/v1
kind: RuntimeClass
metadata:
  name: spin
handler: spin
`

	originalRunner := runtimeClassApplyRunner
	runtimeClassApplyRunner = func(_ *runtimeapi.Facade, _ string, _ bool) (*models.LocalRuntimeClass, error) {
		return nil, &runtimeClassDetailedTestError{
			msg: "failed to apply runtimeclass change through controlled containerd restart: runtime drain before containerd reconfigure failed: timed out draining runtime containers after 45s; remaining container IDs: c1,c2",
			details: map[string]any{
				"stage":                 runtimeapi.RuntimeClassStageStopRuntime,
				"remainingContainerIds": []string{"c1", "c2"},
			},
		}
	}
	t.Cleanup(func() { runtimeClassApplyRunner = originalRunner })

	handler := NewEdgeletAPIHandler()
	applyReq := newManifestMultipartRequest(t, "/v1/deploy/runtimeclasses:apply", manifest, map[string]string{"async": "true"})
	applyRec := httptest.NewRecorder()
	handler.HandleDeployRuntimeClassesApply(applyRec, applyReq)
	if applyRec.Code != http.StatusAccepted {
		t.Fatalf("expected async apply 202, got %d body=%s", applyRec.Code, applyRec.Body.String())
	}

	var applyEnvelope struct {
		Data struct {
			OperationID string `json:"operationId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(applyRec.Body.Bytes(), &applyEnvelope); err != nil {
		t.Fatalf("failed to parse apply response: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		statusReq := httptest.NewRequest(http.MethodGet, "/v1/deploy/runtimeclasses:apply/"+applyEnvelope.Data.OperationID, nil)
		statusRec := httptest.NewRecorder()
		handler.HandleDeployRuntimeClassesApplyStatus(statusRec, statusReq)
		if statusRec.Code != http.StatusOK {
			t.Fatalf("expected poll 200, got %d body=%s", statusRec.Code, statusRec.Body.String())
		}

		var statusEnvelope struct {
			Success bool `json:"success"`
			Data    struct {
				Status string `json:"status"`
				Stage  string `json:"stage"`
				Error  struct {
					Code    string         `json:"code"`
					Message string         `json:"message"`
					Details map[string]any `json:"details"`
				} `json:"error"`
			} `json:"data"`
		}
		if err := json.Unmarshal(statusRec.Body.Bytes(), &statusEnvelope); err != nil {
			t.Fatalf("failed to parse poll response: %v body=%s", err, statusRec.Body.String())
		}
		if !statusEnvelope.Success {
			t.Fatalf("expected poll envelope success=true, body=%s", statusRec.Body.String())
		}
		if statusEnvelope.Data.Status == "failed" {
			if statusEnvelope.Data.Error.Code != ErrCodeInternal {
				t.Fatalf("expected internal error code, got=%s body=%s", statusEnvelope.Data.Error.Code, statusRec.Body.String())
			}
			if statusEnvelope.Data.Stage != runtimeapi.RuntimeClassStageStopRuntime {
				t.Fatalf("expected failed stage %s, got=%s body=%s", runtimeapi.RuntimeClassStageStopRuntime, statusEnvelope.Data.Stage, statusRec.Body.String())
			}
			if !strings.Contains(statusEnvelope.Data.Error.Message, "timed out draining runtime containers") {
				t.Fatalf("expected actionable reconfigure error message, got=%q body=%s", statusEnvelope.Data.Error.Message, statusRec.Body.String())
			}
			if statusEnvelope.Data.Error.Details == nil {
				t.Fatalf("expected structured error details in poll payload, body=%s", statusRec.Body.String())
			}
			if stage, ok := statusEnvelope.Data.Error.Details["stage"].(string); !ok || strings.TrimSpace(stage) != runtimeapi.RuntimeClassStageStopRuntime {
				t.Fatalf("expected error.details.stage=%s, got=%v body=%s", runtimeapi.RuntimeClassStageStopRuntime, statusEnvelope.Data.Error.Details["stage"], statusRec.Body.String())
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("operation did not reach failed state in time, last body=%s", statusRec.Body.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHandleDeployRuntimeClassesApply_SyncFailureIncludesStageDetails(t *testing.T) {
	cfg := setupConfigForGPSTests(t)
	cfg.ContainerEngine = "edgelet"
	embedded := true
	buildmeta.SetHasEmbeddedEngineForTest(&embedded)
	t.Cleanup(func() { buildmeta.SetHasEmbeddedEngineForTest(nil) })

	manifest := `
apiVersion: edgelet.iofog.org/v1
kind: RuntimeClass
metadata:
  name: spin
handler: spin
`

	originalRunner := runtimeClassApplyRunner
	runtimeClassApplyRunner = func(_ *runtimeapi.Facade, _ string, _ bool) (*models.LocalRuntimeClass, error) {
		return nil, &runtimeClassDetailedTestError{
			msg: "forced reconfigure failure",
			details: map[string]any{
				"stage": runtimeapi.RuntimeClassStageStopRuntime,
			},
		}
	}
	t.Cleanup(func() { runtimeClassApplyRunner = originalRunner })

	handler := NewEdgeletAPIHandler()
	applyReq := newManifestMultipartRequest(t, "/v1/deploy/runtimeclasses:apply", manifest, nil)
	applyRec := httptest.NewRecorder()
	handler.HandleDeployRuntimeClassesApply(applyRec, applyReq)
	if applyRec.Code != http.StatusInternalServerError {
		t.Fatalf("expected sync apply failure 500, got %d body=%s", applyRec.Code, applyRec.Body.String())
	}

	var envelope struct {
		Success bool `json:"success"`
		Error   struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(applyRec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("failed to parse sync apply failure response: %v body=%s", err, applyRec.Body.String())
	}
	if envelope.Success {
		t.Fatalf("expected success=false body=%s", applyRec.Body.String())
	}
	if envelope.Error.Code != ErrCodeInternal {
		t.Fatalf("expected internal code, got=%s body=%s", envelope.Error.Code, applyRec.Body.String())
	}
	if stage, ok := envelope.Error.Details["stage"].(string); !ok || strings.TrimSpace(stage) != runtimeapi.RuntimeClassStageStopRuntime {
		t.Fatalf("expected error.details.stage=%s, got=%v body=%s", runtimeapi.RuntimeClassStageStopRuntime, envelope.Error.Details["stage"], applyRec.Body.String())
	}
}

func TestHandleDeployRuntimeClassesApplyStatus_NotFound(t *testing.T) {
	handler := NewEdgeletAPIHandler()
	req := httptest.NewRequest(http.MethodGet, "/v1/deploy/runtimeclasses:apply/not-found", nil)
	rec := httptest.NewRecorder()
	handler.HandleDeployRuntimeClassesApplyStatus(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleDeployRuntimeClassesDelete_AsyncAcceptedAndPollSucceeded(t *testing.T) {
	cfg := setupConfigForGPSTests(t)
	cfg.ContainerEngine = "edgelet"
	embedded := true
	buildmeta.SetHasEmbeddedEngineForTest(&embedded)
	t.Cleanup(func() { buildmeta.SetHasEmbeddedEngineForTest(nil) })

	target := &models.LocalRuntimeClass{
		Name:        "spin",
		Handler:     "spin",
		RuntimeName: "spin",
	}
	originalPreflight := runtimeClassDeletePreflightRunner
	originalRunner := runtimeClassDeleteRunner
	runtimeClassDeletePreflightRunner = func(_ *runtimeapi.Facade, name string) (*models.LocalRuntimeClass, error) {
		if strings.TrimSpace(name) != "spin" {
			return nil, sql.ErrNoRows
		}
		return target, nil
	}
	runtimeClassDeleteRunner = func(_ *runtimeapi.Facade, _ string) error {
		time.Sleep(20 * time.Millisecond)
		return nil
	}
	t.Cleanup(func() {
		runtimeClassDeletePreflightRunner = originalPreflight
		runtimeClassDeleteRunner = originalRunner
	})

	handler := NewEdgeletAPIHandler()
	deleteReq := httptest.NewRequest(http.MethodDelete, "/v1/deploy/runtimeclasses/spin?async=true", nil)
	deleteRec := httptest.NewRecorder()
	handler.HandleDeployRuntimeClasses(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusAccepted {
		t.Fatalf("expected async delete 202, got %d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	var deleteEnvelope struct {
		Success bool `json:"success"`
		Data    struct {
			OperationID string `json:"operationId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(deleteRec.Body.Bytes(), &deleteEnvelope); err != nil {
		t.Fatalf("failed to parse async delete response: %v body=%s", err, deleteRec.Body.String())
	}
	if strings.TrimSpace(deleteEnvelope.Data.OperationID) == "" {
		t.Fatalf("expected operationId in async delete response body=%s", deleteRec.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		statusReq := httptest.NewRequest(http.MethodGet, "/v1/deploy/runtimeclasses:delete/"+deleteEnvelope.Data.OperationID, nil)
		statusRec := httptest.NewRecorder()
		handler.HandleDeployRuntimeClassesDeleteStatus(statusRec, statusReq)
		if statusRec.Code != http.StatusOK {
			t.Fatalf("expected delete poll 200, got %d body=%s", statusRec.Code, statusRec.Body.String())
		}
		var statusEnvelope struct {
			Success bool `json:"success"`
			Data    struct {
				Status string `json:"status"`
				Stage  string `json:"stage"`
			} `json:"data"`
		}
		if err := json.Unmarshal(statusRec.Body.Bytes(), &statusEnvelope); err != nil {
			t.Fatalf("failed to parse delete poll response: %v body=%s", err, statusRec.Body.String())
		}
		if statusEnvelope.Data.Status == "succeeded" {
			if statusEnvelope.Data.Stage != runtimeapi.RuntimeClassStageDone {
				t.Fatalf("expected delete succeeded stage %s, got=%s body=%s", runtimeapi.RuntimeClassStageDone, statusEnvelope.Data.Stage, statusRec.Body.String())
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("delete operation did not reach succeeded state in time, last body=%s", statusRec.Body.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHandleDeployRuntimeClassesDelete_SyncTimeoutReturnsAccepted(t *testing.T) {
	cfg := setupConfigForGPSTests(t)
	cfg.ContainerEngine = "edgelet"
	embedded := true
	buildmeta.SetHasEmbeddedEngineForTest(&embedded)
	t.Cleanup(func() { buildmeta.SetHasEmbeddedEngineForTest(nil) })

	target := &models.LocalRuntimeClass{
		Name:        "spin",
		Handler:     "spin",
		RuntimeName: "spin",
	}
	originalPreflight := runtimeClassDeletePreflightRunner
	originalRunner := runtimeClassDeleteRunner
	originalTimeout := runtimeClassDeleteSyncWaitTimeout
	runtimeClassDeletePreflightRunner = func(_ *runtimeapi.Facade, _ string) (*models.LocalRuntimeClass, error) {
		return target, nil
	}
	runtimeClassDeleteRunner = func(_ *runtimeapi.Facade, _ string) error {
		time.Sleep(80 * time.Millisecond)
		return nil
	}
	runtimeClassDeleteSyncWaitTimeout = 5 * time.Millisecond
	t.Cleanup(func() {
		runtimeClassDeletePreflightRunner = originalPreflight
		runtimeClassDeleteRunner = originalRunner
		runtimeClassDeleteSyncWaitTimeout = originalTimeout
	})

	handler := NewEdgeletAPIHandler()
	deleteReq := httptest.NewRequest(http.MethodDelete, "/v1/deploy/runtimeclasses/spin", nil)
	deleteRec := httptest.NewRecorder()
	handler.HandleDeployRuntimeClasses(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusAccepted {
		t.Fatalf("expected sync delete timeout fallback 202, got %d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
}

func TestHandleDeployRuntimeClassesDelete_RejectsReservedRuntime(t *testing.T) {
	cfg := setupConfigForGPSTests(t)
	cfg.ContainerEngine = "edgelet"
	embedded := true
	buildmeta.SetHasEmbeddedEngineForTest(&embedded)
	t.Cleanup(func() { buildmeta.SetHasEmbeddedEngineForTest(nil) })

	originalPreflight := runtimeClassDeletePreflightRunner
	runtimeClassDeletePreflightRunner = func(_ *runtimeapi.Facade, _ string) (*models.LocalRuntimeClass, error) {
		return nil, &runtimeapi.ErrReservedRuntimeClassDelete{Name: "crun"}
	}
	t.Cleanup(func() { runtimeClassDeletePreflightRunner = originalPreflight })

	handler := NewEdgeletAPIHandler()
	req := httptest.NewRequest(http.MethodDelete, "/v1/deploy/runtimeclasses/crun", nil)
	rec := httptest.NewRecorder()
	handler.HandleDeployRuntimeClasses(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleDeployRuntimeClassesDelete_RejectsInUseRuntimeWithUUIDDetails(t *testing.T) {
	cfg := setupConfigForGPSTests(t)
	cfg.ContainerEngine = "edgelet"
	embedded := true
	buildmeta.SetHasEmbeddedEngineForTest(&embedded)
	t.Cleanup(func() { buildmeta.SetHasEmbeddedEngineForTest(nil) })

	originalPreflight := runtimeClassDeletePreflightRunner
	runtimeClassDeletePreflightRunner = func(_ *runtimeapi.Facade, _ string) (*models.LocalRuntimeClass, error) {
		return nil, &runtimeapi.ErrRuntimeClassInUse{
			Name:                      "spin",
			RuntimeNames:              []string{"spin"},
			BlockingMicroserviceUuids: []string{"4b501939-43b5-4523-a417-577518409df0"},
		}
	}
	t.Cleanup(func() { runtimeClassDeletePreflightRunner = originalPreflight })

	handler := NewEdgeletAPIHandler()
	req := httptest.NewRequest(http.MethodDelete, "/v1/deploy/runtimeclasses/spin", nil)
	rec := httptest.NewRecorder()
	handler.HandleDeployRuntimeClasses(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Success bool `json:"success"`
		Error   struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("failed to parse response: %v body=%s", err, rec.Body.String())
	}
	if envelope.Success {
		t.Fatalf("expected success=false body=%s", rec.Body.String())
	}
	if envelope.Error.Code != ErrCodeInvalidArgument {
		t.Fatalf("expected invalid argument, got=%s body=%s", envelope.Error.Code, rec.Body.String())
	}
	rawUUIDs, ok := envelope.Error.Details["blockingMicroserviceUuids"].([]any)
	if !ok || len(rawUUIDs) == 0 {
		t.Fatalf("expected blockingMicroserviceUuids details, got=%v", envelope.Error.Details)
	}
}

func TestHandleDeployRuntimeClassesDeleteStatus_NotFound(t *testing.T) {
	handler := NewEdgeletAPIHandler()
	req := httptest.NewRequest(http.MethodGet, "/v1/deploy/runtimeclasses:delete/not-found", nil)
	rec := httptest.NewRecorder()
	handler.HandleDeployRuntimeClassesDeleteStatus(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func newManifestMultipartRequest(t *testing.T, targetPath, manifest string, fields map[string]string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	filePart, err := writer.CreateFormFile("manifest", "runtime.yaml")
	if err != nil {
		t.Fatalf("failed to create multipart manifest part: %v", err)
	}
	if _, err := filePart.Write([]byte(strings.TrimSpace(manifest))); err != nil {
		t.Fatalf("failed to write multipart manifest: %v", err)
	}
	for k, v := range fields {
		if err := writer.WriteField(k, v); err != nil {
			t.Fatalf("failed to write multipart field %s: %v", k, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, targetPath, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func generateTestCertPEM(t *testing.T) string {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "test-cert",
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}
