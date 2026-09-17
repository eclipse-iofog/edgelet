package fieldagent

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/auth"
	"github.com/eclipse-iofog/edgelet/internal/buildmeta"
	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/constants"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/utils"
)

func testProvisionPrivateKeyBase64(t *testing.T) string {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	jwkJSON, err := json.Marshal(map[string]any{
		"kty": "OKP",
		"crv": "Ed25519",
		"d":   base64.RawURLEncoding.EncodeToString(privateKey.Seed()),
		"x":   base64.RawURLEncoding.EncodeToString(publicKey),
	})
	if err != nil {
		t.Fatalf("marshal JWK: %v", err)
	}
	return base64.StdEncoding.EncodeToString(jwkJSON)
}

func TestLoadInitialControllerData_LoadsMicroservicesFromController(t *testing.T) {
	openFieldAgentTestDB(t)

	var microservicesRequested atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/agent/registries"):
			_, _ = w.Write([]byte(`{"registries":[]}`))
		case strings.HasSuffix(r.URL.Path, "/agent/volumeMounts"):
			_, _ = w.Write([]byte(`{"volumeMounts":[]}`))
		case strings.HasSuffix(r.URL.Path, "/agent/models"):
			_, _ = w.Write([]byte(`{"models":[]}`))
		case strings.HasSuffix(r.URL.Path, "/agent/microservices"):
			microservicesRequested.Store(true)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"microservices": []map[string]any{
					{"uuid": "ms-reload-1", "imageId": "nginx:latest"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	cfg := config.GetInstance()
	originalURL := cfg.ControllerURL
	cfg.ControllerURL = srv.URL
	t.Cleanup(func() { cfg.ControllerURL = originalURL })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	fa := &FieldAgent{
		config: cfg,
		state:  NewState(),
		apiClient: &APIClient{
			baseURL:    srv.URL,
			httpClient: srv.Client(),
			jwtManager: auth.GetJWTManager(),
		},
		ctx: ctx,
	}
	fa.state.SetControllerStatus(models.ControllerStatusOK)
	fa.state.SetControllerVerified(true)

	fa.loadInitialControllerData(true)

	if !microservicesRequested.Load() {
		t.Fatal("expected GET /agent/microservices during initial controller data load")
	}

	latest := fa.GetLatestMicroservices()
	if len(latest) != 1 || latest[0].MicroserviceUUID != "ms-reload-1" {
		t.Fatalf("expected one loaded microservice, got %#v", latest)
	}
}

func TestProvision_DaemonModeInvokesInitialControllerLoad(t *testing.T) {
	openFieldAgentTestDB(t)

	embedded := false
	buildmeta.SetHasEmbeddedEngineForTest(&embedded)
	t.Cleanup(func() { buildmeta.SetHasEmbeddedEngineForTest(nil) })

	var loadCalled atomic.Bool
	var gotConnected bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/agent/provision"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"uuid":       "agent-uuid-1",
				"privateKey": testProvisionPrivateKeyBase64(t),
				"namespace":  "default",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	cfg := config.GetInstance()
	tmpDir := t.TempDir()
	prevSnapCommon := utils.SNAPCommon
	utils.SNAPCommon = tmpDir
	t.Cleanup(func() { utils.SNAPCommon = prevSnapCommon })

	configPath := filepath.Join(tmpDir, "config.yaml")
	prevConfigDir := utils.ConfigDir
	prevConfigYAMLPath := utils.ConfigYAMLPath
	prevBackupPath := utils.BackupConfigYAMLPath
	utils.ConfigDir = tmpDir + string(os.PathSeparator)
	utils.ConfigYAMLPath = configPath
	utils.BackupConfigYAMLPath = filepath.Join(tmpDir, "config-bck.yaml")
	t.Cleanup(func() {
		utils.ConfigDir = prevConfigDir
		utils.ConfigYAMLPath = prevConfigYAMLPath
		utils.BackupConfigYAMLPath = prevBackupPath
	})
	if err := config.LoadConfig(configPath); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	auth.GetJWTManager().Reset()
	auth.GetLocalTokenManager().Reset()

	originalURL := cfg.ControllerURL
	originalUUID := cfg.IOFogUUID
	originalEngine := cfg.ContainerEngine
	cfg.ControllerURL = srv.URL
	cfg.IOFogUUID = ""
	cfg.ContainerEngine = constants.EngineDocker
	t.Cleanup(func() {
		cfg.ControllerURL = originalURL
		cfg.IOFogUUID = originalUUID
		cfg.PrivateKey = ""
		cfg.ContainerEngine = originalEngine
		auth.GetJWTManager().Reset()
		auth.GetLocalTokenManager().Reset()
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	fa := &FieldAgent{
		config: cfg,
		state:  NewState(),
		apiClient: &APIClient{
			baseURL:    srv.URL,
			httpClient: srv.Client(),
			jwtManager: auth.GetJWTManager(),
		},
		ctx: ctx,
	}
	fa.loadInitialControllerDataHook = func(isConnected bool) {
		loadCalled.Store(true)
		gotConnected = isConnected
	}

	if err := fa.Provision("provision-key"); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	if !loadCalled.Load() {
		t.Fatal("expected live provision on running daemon to invoke loadInitialControllerData")
	}
	if !gotConnected {
		t.Fatal("expected loadInitialControllerData with controller connected after provision")
	}
}
