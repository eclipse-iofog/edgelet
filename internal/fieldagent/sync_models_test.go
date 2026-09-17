package fieldagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/auth"
	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/store"
)

func fixtureControllerModelsJSON() []byte {
	return []byte(`{"models":[{
		"uuid":"3f2c8a1e-2b64-4c0d-9f11-0a1b2c3d4e5f",
		"name":"test-model",
		"repo":"second-state/Llama-2-7B-Chat-GGUF",
		"revision":"064fe43ea8c1e1f93477ef4a170bdc2b244ef02c",
		"registryId":5,
		"files":["llama-2-7b-chat.Q5_K_M.gguf"],
		"format":"gguf",
		"id":99
	}]}`)
}

func newModelsFieldAgent(t *testing.T, handler http.HandlerFunc) *FieldAgent {
	t.Helper()
	openFieldAgentTestDB(t)

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	cfg := config.GetInstance()
	origURL := cfg.ControllerURL
	origUUID := cfg.IOFogUUID
	origKey := cfg.PrivateKey
	cfg.ControllerURL = srv.URL
	cfg.IOFogUUID = "uuid-models-sync"
	cfg.PrivateKey = testProvisionPrivateKeyBase64(t)
	t.Cleanup(func() {
		cfg.ControllerURL = origURL
		cfg.IOFogUUID = origUUID
		cfg.PrivateKey = origKey
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	fa := &FieldAgent{
		config: cfg,
		state:  NewState(),
		ctx:    ctx,
		apiClient: &APIClient{
			baseURL:    srv.URL,
			httpClient: srv.Client(),
			jwtManager: auth.GetJWTManager(),
		},
	}
	fa.state.SetControllerStatus(models.ControllerStatusOK)
	fa.state.SetControllerVerified(true)
	return fa
}

func TestLoadModels_TrueFlagReplaceAllUUIDName(t *testing.T) {
	var modelsRequested atomic.Bool
	fa := newModelsFieldAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/agent/models") {
			modelsRequested.Store(true)
			_, _ = w.Write(fixtureControllerModelsJSON())
			return
		}
		http.NotFound(w, r)
	})

	if err := fa.loadModels(false); err != nil {
		t.Fatalf("loadModels: %v", err)
	}
	if !modelsRequested.Load() {
		t.Fatal("expected GET models")
	}

	items, err := store.GetInstance().LoadControllerModels()
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	if len(items) != 1 || items[0].UUID != "3f2c8a1e-2b64-4c0d-9f11-0a1b2c3d4e5f" || items[0].Name != "test-model" {
		t.Fatalf("unexpected controller models: %+v", items)
	}

	row, err := store.GetInstance().GetLocalModel("test-model")
	if err != nil {
		t.Fatalf("local model: %v", err)
	}
	if row.Source != models.ModelSourceManaged {
		t.Fatalf("expected managed source, got %q", row.Source)
	}
}

func TestProcessChanges_ModelsFlagFalseOnInitializationStillLoadsModels(t *testing.T) {
	var modelsRequested atomic.Bool
	fa := newModelsFieldAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/agent/models") {
			modelsRequested.Store(true)
			_, _ = w.Write(fixtureControllerModelsJSON())
			return
		}
		http.NotFound(w, r)
	})
	fa.state.SetInitialization(true)

	_ = fa.processChanges(map[string]any{"models": false})
	if !modelsRequested.Load() {
		t.Fatal("models flag false on initialization still loads models, matching registries and volume mounts")
	}

	items, err := store.GetInstance().LoadControllerModels()
	if err != nil || len(items) != 1 {
		t.Fatalf("expected one stored model after init load, got %+v err=%v", items, err)
	}
}

func TestProcessChanges_ModelsTrueLoadsList(t *testing.T) {
	var modelsRequested atomic.Bool
	fa := newModelsFieldAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/agent/models") {
			modelsRequested.Store(true)
			_, _ = w.Write(fixtureControllerModelsJSON())
			return
		}
		http.NotFound(w, r)
	})
	fa.state.SetInitialization(false)

	_ = fa.processChanges(map[string]any{"models": true})
	if !modelsRequested.Load() {
		t.Fatal("expected GET models when models flag is true")
	}
}

func TestGetFogStatus_ModelKeysPresentAndIgnoredSafeWhenEmpty(t *testing.T) {
	openFieldAgentTestDB(t)
	fa := &FieldAgent{
		config: config.GetInstance(),
		state:  NewState(),
	}
	status := fa.getFogStatus()
	raw, ok := status["modelStatus"].(string)
	if !ok || raw != "[]" {
		t.Fatalf("expected empty modelStatus string, got %#v", status["modelStatus"])
	}
	if status["activeModels"] != 0 {
		t.Fatalf("expected activeModels 0, got %#v", status["activeModels"])
	}
	if status["modelLastUpdate"] != int64(0) {
		t.Fatalf("expected modelLastUpdate 0, got %#v", status["modelLastUpdate"])
	}
}

func TestGetFogStatus_ReadyManagedModelIncludesUUIDNameState(t *testing.T) {
	openFieldAgentTestDB(t)

	item := &models.ControllerModel{
		UUID:       "3f2c8a1e-2b64-4c0d-9f11-0a1b2c3d4e5f",
		Name:       "test-model",
		Repo:       "second-state/Llama-2-7B-Chat-GGUF",
		Revision:   "064fe43",
		RegistryID: 5,
	}
	if err := store.GetInstance().SaveControllerModels([]*models.ControllerModel{item}); err != nil {
		t.Fatalf("save controller models: %v", err)
	}
	row := item.ToLocalModel()
	row.State = models.ModelStateReady
	row.Digest = "sha256:abc"
	row.ResolvedRevision = "064fe43"
	row.TotalBytes = 2840000000
	row.LastTransitionAt = 1700000000
	row.LastReconcileAt = 1700000000
	if err := store.GetInstance().UpsertLocalModel(row); err != nil {
		t.Fatalf("upsert local: %v", err)
	}

	localOnly := &models.LocalModel{
		Name:       "operator-only",
		Source:     models.ModelSourceLocal,
		Repo:       "org/local",
		RegistryID: 1,
		State:      models.ModelStateReady,
	}
	localOnly.NormalizeDefaults()
	localOnly.LastTransitionAt = 1600000000
	localOnly.LastReconcileAt = 1600000000
	if err := store.GetInstance().UpsertLocalModel(localOnly); err != nil {
		t.Fatalf("upsert local-only: %v", err)
	}

	fa := &FieldAgent{
		config: config.GetInstance(),
		state:  NewState(),
	}
	status := fa.getFogStatus()
	if status["activeModels"] != 1 {
		t.Fatalf("expected one managed model, got %#v", status["activeModels"])
	}
	raw, ok := status["modelStatus"].(string)
	if !ok {
		t.Fatalf("modelStatus type: %#v", status["modelStatus"])
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		t.Fatalf("parse modelStatus: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected local and managed models, got %#v", items)
	}
	byName := map[string]map[string]any{}
	for _, item := range items {
		name, ok := item["name"].(string)
		if !ok {
			t.Fatalf("expected name string, got %#v", item["name"])
		}
		byName[name] = item
	}
	managed := byName["test-model"]
	if managed == nil {
		t.Fatalf("missing managed model: %#v", items)
	}
	if managed["uuid"] != item.UUID || managed["source"] != models.ModelSourceManaged || managed["state"] != models.ModelStateReady {
		t.Fatalf("unexpected managed modelStatus item: %#v", managed)
	}
	local := byName["operator-only"]
	if local == nil {
		t.Fatalf("missing local model: %#v", items)
	}
	if local["source"] != models.ModelSourceLocal {
		t.Fatalf("expected source=local, got %#v", local)
	}
	if _, ok := local["uuid"]; ok {
		t.Fatalf("local modelStatus must omit uuid, got %#v", local)
	}
	if status["modelLastUpdate"] != int64(1700000000) {
		t.Fatalf("modelLastUpdate=%#v", status["modelLastUpdate"])
	}
}

func TestLoadInitialControllerData_LoadsModelsBeforeMicroservices(t *testing.T) {
	var order []string
	fa := newModelsFieldAgent(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/agent/registries"):
			order = append(order, "registries")
			_, _ = w.Write([]byte(`{"registries":[]}`))
		case strings.HasSuffix(r.URL.Path, "/agent/volumeMounts"):
			order = append(order, "volumeMounts")
			_, _ = w.Write([]byte(`{"volumeMounts":[]}`))
		case strings.HasSuffix(r.URL.Path, "/agent/models"):
			order = append(order, "models")
			_, _ = w.Write(fixtureControllerModelsJSON())
		case strings.HasSuffix(r.URL.Path, "/agent/runtimeClasses"):
			order = append(order, "runtimeClasses")
			_, _ = w.Write([]byte(`{"runtimeClasses":[]}`))
		case strings.HasSuffix(r.URL.Path, "/agent/microservices"):
			order = append(order, "microservices")
			_, _ = w.Write([]byte(`{"microservices":[]}`))
		default:
			http.NotFound(w, r)
		}
	})

	fa.loadInitialControllerData(true)

	modelsAt, msAt := -1, -1
	for i, name := range order {
		if name == "models" {
			modelsAt = i
		}
		if name == "microservices" {
			msAt = i
		}
	}
	if modelsAt < 0 || msAt < 0 || modelsAt > msAt {
		t.Fatalf("expected models before microservices, order=%v", order)
	}
}

func TestGetFogStatus_LocalOnlyModelsOmitUUIDAndActiveModelsZero(t *testing.T) {
	openFieldAgentTestDB(t)
	localOnly := &models.LocalModel{
		Name:       "operator-only",
		Source:     models.ModelSourceLocal,
		Repo:       "org/local",
		RegistryID: 1,
		State:      models.ModelStateReady,
	}
	localOnly.NormalizeDefaults()
	if err := store.GetInstance().UpsertLocalModel(localOnly); err != nil {
		t.Fatalf("upsert local-only: %v", err)
	}

	fa := &FieldAgent{
		config: config.GetInstance(),
		state:  NewState(),
	}
	status := fa.getFogStatus()
	if status["activeModels"] != 0 {
		t.Fatalf("expected activeModels 0 for local-only, got %#v", status["activeModels"])
	}
	raw, ok := status["modelStatus"].(string)
	if !ok {
		t.Fatalf("modelStatus type: %#v", status["modelStatus"])
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		t.Fatalf("parse modelStatus: %v", err)
	}
	if len(items) != 1 || items[0]["name"] != "operator-only" || items[0]["source"] != models.ModelSourceLocal {
		t.Fatalf("unexpected local-only modelStatus: %#v", items)
	}
	if _, hasUUID := items[0]["uuid"]; hasUUID {
		t.Fatalf("local item must omit uuid: %#v", items[0])
	}
}
