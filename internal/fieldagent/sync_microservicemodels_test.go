package fieldagent

import (
	"context"
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

func fixtureControllerMicroservicesJSON() []byte {
	return []byte(`{"microservices":[{
		"uuid":"ms-catalog",
		"imageId":"alpine:3.19",
		"models":{
			"bindPath":"/models",
			"permissions":"ro",
			"items":[{"name":"test-model"}]
		}
	}]}`)
}

func newMicroservicesFieldAgent(t *testing.T, handler http.HandlerFunc) *FieldAgent {
	t.Helper()
	openFieldAgentTestDB(t)

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	cfg := config.GetInstance()
	origURL := cfg.ControllerURL
	origUUID := cfg.IOFogUUID
	origKey := cfg.PrivateKey
	cfg.ControllerURL = srv.URL
	cfg.IOFogUUID = "uuid-ms-models-sync"
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

func TestProcessChanges_MicroserviceModelsFlagLoadsMicroservices(t *testing.T) {
	var gets atomic.Int32
	fa := newMicroservicesFieldAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/agent/microservices") {
			gets.Add(1)
			_, _ = w.Write(fixtureControllerMicroservicesJSON())
			return
		}
		http.NotFound(w, r)
	})
	fa.state.SetInitialization(false)

	_ = fa.processChanges(map[string]any{"microserviceModels": true})
	if gets.Load() != 1 {
		t.Fatalf("expected one GET microservices, got %d", gets.Load())
	}

	items, err := store.GetInstance().LoadControllerMicroservices()
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	if len(items) != 1 || items[0].MicroserviceUUID != "ms-catalog" {
		t.Fatalf("unexpected stored microservices: %+v", items)
	}
	if !items[0].Models.HasItems() || items[0].Models.BindPath != "/models" || items[0].Models.Items[0].Name != "test-model" {
		t.Fatalf("expected catalog persisted, got %+v", items[0].Models)
	}
}

func TestProcessChanges_MicroserviceListAndModelsShareOneGET(t *testing.T) {
	var gets atomic.Int32
	fa := newMicroservicesFieldAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/agent/microservices") {
			gets.Add(1)
			_, _ = w.Write(fixtureControllerMicroservicesJSON())
			return
		}
		http.NotFound(w, r)
	})
	fa.state.SetInitialization(false)

	_ = fa.processChanges(map[string]any{
		"microserviceList":   true,
		"microserviceModels": true,
		"unknownExtraFlag":   true,
	})
	if gets.Load() != 1 {
		t.Fatalf("list and catalog flags must share one GET, got %d", gets.Load())
	}
}

func TestProcessChanges_MicroserviceModelsFalseDoesNotFetch(t *testing.T) {
	var gets atomic.Int32
	fa := newMicroservicesFieldAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/agent/microservices") {
			gets.Add(1)
			_, _ = w.Write(fixtureControllerMicroservicesJSON())
			return
		}
		http.NotFound(w, r)
	})
	fa.state.SetInitialization(false)

	_ = fa.processChanges(map[string]any{"microserviceModels": false})
	if gets.Load() != 0 {
		t.Fatalf("false catalog flag must not GET microservices, got %d", gets.Load())
	}
}
