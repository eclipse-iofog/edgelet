package fieldagent

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/auth"
	"github.com/eclipse-iofog/edgelet/internal/buildmeta"
	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/constants"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/store"
)

func fixtureControllerRuntimeClassesJSON() []byte {
	return []byte(`{"runtimeClasses":[
		{"name":"spin","handler":"spin","extra":true},
		{"name":"nvidia","handler":"nvidia"}
	]}`)
}

func newRuntimeClassFieldAgent(t *testing.T, handler http.HandlerFunc) *FieldAgent {
	t.Helper()
	openFieldAgentTestDB(t)

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	cfg := config.GetInstance()
	origURL := cfg.ControllerURL
	origUUID := cfg.IOFogUUID
	origKey := cfg.PrivateKey
	origEngine := cfg.ContainerEngine
	cfg.ControllerURL = srv.URL
	cfg.IOFogUUID = "uuid-runtimeclass-sync"
	cfg.PrivateKey = testProvisionPrivateKeyBase64(t)
	cfg.ContainerEngine = constants.EngineEdgelet
	embedded := true
	buildmeta.SetHasEmbeddedEngineForTest(&embedded)
	t.Cleanup(func() {
		cfg.ControllerURL = origURL
		cfg.IOFogUUID = origUUID
		cfg.PrivateKey = origKey
		cfg.ContainerEngine = origEngine
		buildmeta.SetHasEmbeddedEngineForTest(nil)
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

func TestLoadRuntimeClasses_TrueFlagReplaceAllDesiredRows(t *testing.T) {
	var requested atomic.Bool
	fa := newRuntimeClassFieldAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/agent/runtimeClasses") {
			requested.Store(true)
			_, _ = w.Write(fixtureControllerRuntimeClassesJSON())
			return
		}
		http.NotFound(w, r)
	})

	if err := fa.loadRuntimeClasses(false); err != nil {
		t.Fatalf("loadRuntimeClasses: %v", err)
	}
	if !requested.Load() {
		t.Fatal("expected GET runtimeClasses")
	}

	items, err := store.GetInstance().LoadControllerRuntimeClasses()
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	if len(items) != 2 || items[0].Name != "nvidia" || items[1].Name != "spin" {
		t.Fatalf("expected replace-all sorted desired rows, got %+v", items)
	}

	spin, err := store.GetInstance().GetLocalRuntimeClass("spin")
	if err != nil {
		t.Fatalf("applied spin: %v", err)
	}
	if spin.Source != models.RuntimeClassSourceManaged || spin.Handler != "spin" {
		t.Fatalf("expected managed spin apply, got %+v", spin)
	}
}

func TestProcessChanges_RuntimeClassesFlagFalseOnInitializationStillLoads(t *testing.T) {
	var requested atomic.Bool
	fa := newRuntimeClassFieldAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/agent/runtimeClasses") {
			requested.Store(true)
			_, _ = w.Write(fixtureControllerRuntimeClassesJSON())
			return
		}
		http.NotFound(w, r)
	})
	fa.state.SetInitialization(true)

	_ = fa.processChanges(map[string]any{"runtimeClasses": false})
	if !requested.Load() {
		t.Fatal("runtimeClasses flag false on initialization still loads, matching models and registries")
	}
}

func TestProcessChanges_RuntimeClassesTrueLoadsList(t *testing.T) {
	var requested atomic.Bool
	fa := newRuntimeClassFieldAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/agent/runtimeClasses") {
			requested.Store(true)
			_, _ = w.Write(fixtureControllerRuntimeClassesJSON())
			return
		}
		http.NotFound(w, r)
	})
	fa.state.SetInitialization(false)

	_ = fa.processChanges(map[string]any{"runtimeClasses": true})
	if !requested.Load() {
		t.Fatal("expected GET runtimeClasses when runtimeClasses flag is true")
	}
}

func TestLoadRuntimeClasses_MissingEndpointEmptyListContinues(t *testing.T) {
	fa := newRuntimeClassFieldAgent(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	if err := store.GetInstance().SaveControllerRuntimeClasses([]*models.ControllerRuntimeClass{
		{Name: "spin", Handler: "spin"},
	}); err != nil {
		t.Fatalf("seed desired: %v", err)
	}

	if err := fa.loadRuntimeClasses(false); err != nil {
		t.Fatalf("missing endpoint must not fail the node: %v", err)
	}
	items, err := store.GetInstance().LoadControllerRuntimeClasses()
	if err != nil || len(items) != 0 {
		t.Fatalf("expected empty desired set after missing endpoint, got %+v err=%v", items, err)
	}
}

func TestLoadRuntimeClasses_DockerEnginePersistsDesiredWithoutApply(t *testing.T) {
	var requested atomic.Bool
	fa := newRuntimeClassFieldAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/agent/runtimeClasses") {
			requested.Store(true)
			_, _ = w.Write(fixtureControllerRuntimeClassesJSON())
			return
		}
		http.NotFound(w, r)
	})
	fa.config.ContainerEngine = constants.EngineDocker

	if err := fa.loadRuntimeClasses(false); err != nil {
		t.Fatalf("docker engine must not fail the node: %v", err)
	}
	if !requested.Load() {
		t.Fatal("expected GET runtimeClasses on docker engine")
	}
	items, err := store.GetInstance().LoadControllerRuntimeClasses()
	if err != nil || len(items) != 2 {
		t.Fatalf("expected desired rows persisted on docker, got %+v err=%v", items, err)
	}
	if _, err := store.GetInstance().GetLocalRuntimeClass("spin"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("docker engine must not apply local runtime classes, got %v", err)
	}
}

func TestLoadRuntimeClasses_DropsManagedNameKeepsLocalOnly(t *testing.T) {
	fa := newRuntimeClassFieldAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/agent/runtimeClasses") {
			_, _ = w.Write([]byte(`{"runtimeClasses":[]}`))
			return
		}
		http.NotFound(w, r)
	})

	if err := store.GetInstance().UpsertLocalRuntimeClass(&models.LocalRuntimeClass{
		Name:    "spin",
		Handler: "spin",
		Source:  models.RuntimeClassSourceManaged,
	}); err != nil {
		t.Fatalf("seed managed: %v", err)
	}
	if err := store.GetInstance().UpsertLocalRuntimeClass(&models.LocalRuntimeClass{
		Name:    "operator-only",
		Handler: "wasmedge",
		Source:  models.RuntimeClassSourceLocal,
	}); err != nil {
		t.Fatalf("seed local-only: %v", err)
	}

	if err := fa.loadRuntimeClasses(false); err != nil {
		t.Fatalf("loadRuntimeClasses: %v", err)
	}
	if _, err := store.GetInstance().GetLocalRuntimeClass("spin"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected dropped managed name deleted, got %v", err)
	}
	localOnly, err := store.GetInstance().GetLocalRuntimeClass("operator-only")
	if err != nil || localOnly.Source != models.RuntimeClassSourceLocal {
		t.Fatalf("expected local-only name kept, got %+v err=%v", localOnly, err)
	}
}

func TestLoadRuntimeClasses_InUseDeleteRefused(t *testing.T) {
	fa := newRuntimeClassFieldAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/agent/runtimeClasses") {
			_, _ = w.Write([]byte(`{"runtimeClasses":[]}`))
			return
		}
		http.NotFound(w, r)
	})

	if err := store.GetInstance().UpsertLocalRuntimeClass(&models.LocalRuntimeClass{
		Name:    "spin",
		Handler: "spin",
		Source:  models.RuntimeClassSourceManaged,
	}); err != nil {
		t.Fatalf("seed managed: %v", err)
	}
	ms := &models.LocalDeployedMicroservice{
		LocalUUID:    "11111111-1111-1111-1111-111111111111",
		RuntimeState: "running",
		ManifestYAML: `apiVersion: edgelet.iofog.org/v1
kind: Microservice
spec:
  container:
    runtime: spin
`,
	}
	if err := store.GetInstance().UpsertLocalWorkload(ms); err != nil {
		t.Fatalf("seed workload: %v", err)
	}

	if err := fa.loadRuntimeClasses(false); err != nil {
		t.Fatalf("in-use delete must not fail the node: %v", err)
	}
	got, err := store.GetInstance().GetLocalRuntimeClass("spin")
	if err != nil || got.Source != models.RuntimeClassSourceManaged {
		t.Fatalf("expected in-use managed class kept, got %+v err=%v", got, err)
	}
}

func TestLoadInitialControllerData_LoadsRuntimeClassesBeforeMicroservices(t *testing.T) {
	var order []string
	fa := newRuntimeClassFieldAgent(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/agent/registries"):
			order = append(order, "registries")
			_, _ = w.Write([]byte(`{"registries":[]}`))
		case strings.HasSuffix(r.URL.Path, "/agent/volumeMounts"):
			order = append(order, "volumeMounts")
			_, _ = w.Write([]byte(`{"volumeMounts":[]}`))
		case strings.HasSuffix(r.URL.Path, "/agent/models"):
			order = append(order, "models")
			_, _ = w.Write([]byte(`{"models":[]}`))
		case strings.HasSuffix(r.URL.Path, "/agent/runtimeClasses"):
			order = append(order, "runtimeClasses")
			_, _ = w.Write(fixtureControllerRuntimeClassesJSON())
		case strings.HasSuffix(r.URL.Path, "/agent/microservices"):
			order = append(order, "microservices")
			_, _ = w.Write([]byte(`{"microservices":[]}`))
		default:
			http.NotFound(w, r)
		}
	})

	fa.loadInitialControllerData(true)

	rcAt, msAt := -1, -1
	for i, name := range order {
		if name == "runtimeClasses" {
			rcAt = i
		}
		if name == "microservices" {
			msAt = i
		}
	}
	if rcAt < 0 || msAt < 0 || rcAt > msAt {
		t.Fatalf("expected runtime classes before microservices, order=%v", order)
	}
}

func TestControllerReconcile_RefreshesRuntimeClasses(t *testing.T) {
	var requested atomic.Bool
	fa := newRuntimeClassFieldAgent(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/agent/registries"):
			_, _ = w.Write([]byte(`{"registries":[]}`))
		case strings.HasSuffix(r.URL.Path, "/agent/volumeMounts"):
			_, _ = w.Write([]byte(`{"volumeMounts":[]}`))
		case strings.HasSuffix(r.URL.Path, "/agent/models"):
			_, _ = w.Write([]byte(`{"models":[]}`))
		case strings.HasSuffix(r.URL.Path, "/agent/runtimeClasses"):
			requested.Store(true)
			_, _ = w.Write(fixtureControllerRuntimeClassesJSON())
		case strings.HasSuffix(r.URL.Path, "/agent/microservices"):
			_, _ = w.Write([]byte(`{"microservices":[]}`))
		default:
			http.NotFound(w, r)
		}
	})

	if err := fa.controllerReconcile(); err != nil {
		t.Fatalf("controllerReconcile: %v", err)
	}
	if !requested.Load() {
		t.Fatal("expected reconnect reconcile to GET runtimeClasses")
	}
}
