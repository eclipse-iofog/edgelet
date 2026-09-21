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
	"github.com/eclipse-iofog/edgelet/internal/knowledgecatalog"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/store"
)

func fixtureControllerKnowledgeJSON() []byte {
	return []byte(`{"knowledge":[{
		"uuid":"3f2c1111-2222-3333-4444-555566667777",
		"name":"product-docs",
		"repo":"acme/product-manuals",
		"revision":"9f3c111122223333444455556666777788889999",
		"registryId":3,
		"files":["data/**/*.jsonl"],
		"format":"jsonl",
		"id":99
	}]}`)
}

func newKnowledgeFieldAgent(t *testing.T, handler http.HandlerFunc) *FieldAgent {
	t.Helper()
	openFieldAgentTestDB(t)

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	cfg := config.GetInstance()
	origURL := cfg.ControllerURL
	origUUID := cfg.IOFogUUID
	origKey := cfg.PrivateKey
	cfg.ControllerURL = srv.URL
	cfg.IOFogUUID = "uuid-knowledge-sync"
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

func TestLoadKnowledge_TrueFlagReplaceAllUUIDName(t *testing.T) {
	var knowledgeRequested atomic.Bool
	fa := newKnowledgeFieldAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/agent/knowledge") {
			knowledgeRequested.Store(true)
			_, _ = w.Write(fixtureControllerKnowledgeJSON())
			return
		}
		http.NotFound(w, r)
	})

	if err := fa.loadKnowledge(false); err != nil {
		t.Fatalf("loadKnowledge: %v", err)
	}
	if !knowledgeRequested.Load() {
		t.Fatal("expected GET knowledge")
	}

	items, err := store.GetInstance().LoadControllerKnowledge()
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	if len(items) != 1 || items[0].UUID != "3f2c1111-2222-3333-4444-555566667777" || items[0].Name != "product-docs" {
		t.Fatalf("unexpected controller knowledge: %+v", items)
	}

	row, err := store.GetInstance().GetLocalKnowledge("product-docs")
	if err != nil {
		t.Fatalf("local knowledge: %v", err)
	}
	if row.Source != models.KnowledgeSourceManaged {
		t.Fatalf("expected managed source, got %q", row.Source)
	}
}

func TestProcessChanges_KnowledgeTrueLoadsList(t *testing.T) {
	var knowledgeRequested atomic.Bool
	fa := newKnowledgeFieldAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/agent/knowledge") {
			knowledgeRequested.Store(true)
			_, _ = w.Write(fixtureControllerKnowledgeJSON())
			return
		}
		http.NotFound(w, r)
	})
	fa.state.SetInitialization(false)

	_ = fa.processChanges(map[string]any{"knowledge": true})
	if !knowledgeRequested.Load() {
		t.Fatal("expected GET knowledge when knowledge flag is true")
	}
}

func TestProcessChanges_KnowledgeFlagFalseOnInitializationStillLoadsKnowledge(t *testing.T) {
	var knowledgeRequested atomic.Bool
	fa := newKnowledgeFieldAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/agent/knowledge") {
			knowledgeRequested.Store(true)
			_, _ = w.Write(fixtureControllerKnowledgeJSON())
			return
		}
		http.NotFound(w, r)
	})
	fa.state.SetInitialization(true)

	_ = fa.processChanges(map[string]any{"knowledge": false})
	if !knowledgeRequested.Load() {
		t.Fatal("knowledge flag false on initialization still loads knowledge, matching models")
	}
}

func TestLoadKnowledge_MissingEndpointUsesEmptyList(t *testing.T) {
	fa := newKnowledgeFieldAgent(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	if err := fa.loadKnowledge(false); err != nil {
		t.Fatalf("missing knowledge endpoint must not fail the node: %v", err)
	}
	items, err := store.GetInstance().LoadControllerKnowledge()
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected empty controller knowledge, got %+v", items)
	}
}

func TestLoadKnowledge_ManagedNameRejectsLocalApply(t *testing.T) {
	fa := newKnowledgeFieldAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/agent/knowledge") {
			_, _ = w.Write(fixtureControllerKnowledgeJSON())
			return
		}
		http.NotFound(w, r)
	})
	if err := fa.loadKnowledge(false); err != nil {
		t.Fatalf("loadKnowledge: %v", err)
	}

	doc := &models.LocalKnowledgeManifest{
		APIVersion: "edgelet.iofog.org/v1",
		Kind:       "Knowledge",
		Spec: models.LocalKnowledgeSpec{
			Repo:     "org/other",
			Registry: 3,
		},
	}
	doc.Metadata.Name = "product-docs"
	_, err := fa.knowledgeManager().ApplyManifest(doc)
	if err == nil || !strings.Contains(err.Error(), "controller-managed") {
		t.Fatalf("expected local apply to be rejected for managed name, got %v", err)
	}
}

func TestParseControllerKnowledge_SkipsMissingIdentityAndIgnoresUnknownKeys(t *testing.T) {
	items := parseControllerKnowledgeList(map[string]any{
		"knowledge": []any{
			map[string]any{"name": "no-uuid", "repo": "org/a"},
			map[string]any{"uuid": "u-1"},
			map[string]any{
				"uuid":           "3f2c1111-2222-3333-4444-555566667777",
				"name":           "product-docs",
				"repo":           "acme/product-manuals",
				"registryId":     float64(3),
				"futureExtraKey": true,
			},
		},
	})
	if len(items) != 1 || items[0].Name != "product-docs" || items[0].UUID != "3f2c1111-2222-3333-4444-555566667777" {
		t.Fatalf("expected one valid knowledge row, got %+v", items)
	}
}

func TestGetFogStatus_KnowledgeKeysPresentAndIgnoredSafeWhenEmpty(t *testing.T) {
	openFieldAgentTestDB(t)
	fa := &FieldAgent{
		config: config.GetInstance(),
		state:  NewState(),
	}
	status := fa.getFogStatus()
	raw, ok := status["knowledgeStatus"].(string)
	if !ok || raw != "[]" {
		t.Fatalf("expected empty knowledgeStatus string, got %#v", status["knowledgeStatus"])
	}
	if status["activeKnowledge"] != 0 {
		t.Fatalf("expected activeKnowledge 0, got %#v", status["activeKnowledge"])
	}
	if status["knowledgeLastUpdate"] != int64(0) {
		t.Fatalf("expected knowledgeLastUpdate 0, got %#v", status["knowledgeLastUpdate"])
	}
}

func TestGetFogStatus_KnowledgeStatusLocalOmitsUUIDAndActiveIsManagedCount(t *testing.T) {
	openFieldAgentTestDB(t)

	item := &models.ControllerKnowledge{
		UUID:       "3f2c1111-2222-3333-4444-555566667777",
		Name:       "product-docs",
		Repo:       "acme/product-manuals",
		Revision:   "9f3c1111",
		RegistryID: 3,
	}
	if err := store.GetInstance().SaveControllerKnowledge([]*models.ControllerKnowledge{item}); err != nil {
		t.Fatalf("save controller knowledge: %v", err)
	}
	row := item.ToLocalKnowledge()
	row.State = models.KnowledgeStateReady
	row.Digest = "sha256:abc"
	row.ResolvedRevision = "9f3c1111"
	row.TotalBytes = 128000000
	if err := store.GetInstance().UpsertLocalKnowledge(row); err != nil {
		t.Fatalf("upsert local: %v", err)
	}

	localOnly := &models.LocalKnowledge{
		Name:       "operator-docs",
		Source:     models.KnowledgeSourceLocal,
		Repo:       "org/local",
		RegistryID: 1,
		State:      models.KnowledgeStateReady,
	}
	localOnly.NormalizeDefaults()
	if err := store.GetInstance().UpsertLocalKnowledge(localOnly); err != nil {
		t.Fatalf("upsert local-only: %v", err)
	}

	fa := &FieldAgent{
		config:              config.GetInstance(),
		state:               NewState(),
		knowledgeLastUpdate: 1726660000123,
	}
	status := fa.getFogStatus()
	if status["activeKnowledge"] != 1 {
		t.Fatalf("expected one managed knowledge, got %#v", status["activeKnowledge"])
	}
	raw, ok := status["knowledgeStatus"].(string)
	if !ok {
		t.Fatalf("knowledgeStatus type: %#v", status["knowledgeStatus"])
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		t.Fatalf("parse knowledgeStatus: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected local and managed knowledge, got %#v", items)
	}
	byName := map[string]map[string]any{}
	for _, statusItem := range items {
		name, ok := statusItem["name"].(string)
		if !ok {
			t.Fatalf("expected name string, got %#v", statusItem["name"])
		}
		byName[name] = statusItem
	}
	managed := byName["product-docs"]
	if managed == nil {
		t.Fatalf("missing managed knowledge: %#v", items)
	}
	if managed["uuid"] != item.UUID || managed["source"] != models.KnowledgeSourceManaged || managed["state"] != models.KnowledgeStateReady {
		t.Fatalf("unexpected managed knowledgeStatus item: %#v", managed)
	}
	local := byName["operator-docs"]
	if local == nil {
		t.Fatalf("missing local knowledge: %#v", items)
	}
	if local["source"] != models.KnowledgeSourceLocal {
		t.Fatalf("expected source=local, got %#v", local)
	}
	if _, ok := local["uuid"]; ok {
		t.Fatalf("local knowledgeStatus must omit uuid, got %#v", local)
	}
	if status["knowledgeLastUpdate"] != int64(1726660000123) {
		t.Fatalf("knowledgeLastUpdate=%#v", status["knowledgeLastUpdate"])
	}
}

func TestGetFogStatus_ActiveKnowledgeZeroForLocalOnly(t *testing.T) {
	openFieldAgentTestDB(t)
	localOnly := &models.LocalKnowledge{
		Name:       "operator-docs",
		Source:     models.KnowledgeSourceLocal,
		Repo:       "org/local",
		RegistryID: 1,
		State:      models.KnowledgeStateReady,
	}
	localOnly.NormalizeDefaults()
	if err := store.GetInstance().UpsertLocalKnowledge(localOnly); err != nil {
		t.Fatalf("upsert local-only: %v", err)
	}

	fa := &FieldAgent{
		config: config.GetInstance(),
		state:  NewState(),
	}
	status := fa.getFogStatus()
	if status["activeKnowledge"] != 0 {
		t.Fatalf("expected activeKnowledge 0 for local-only, got %#v", status["activeKnowledge"])
	}
	raw, ok := status["knowledgeStatus"].(string)
	if !ok {
		t.Fatalf("knowledgeStatus type: %#v", status["knowledgeStatus"])
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		t.Fatalf("parse knowledgeStatus: %v", err)
	}
	if len(items) != 1 || items[0]["name"] != "operator-docs" || items[0]["source"] != models.KnowledgeSourceLocal {
		t.Fatalf("unexpected local-only knowledgeStatus: %#v", items)
	}
	if _, hasUUID := items[0]["uuid"]; hasUUID {
		t.Fatalf("local item must omit uuid: %#v", items[0])
	}
}

func TestPostStatusHelper_KnowledgeKeysNeverFailStatus(t *testing.T) {
	openFieldAgentTestDB(t)
	var sent map[string]any
	fa := &FieldAgent{
		config: config.GetInstance(),
		state:  NewState(),
		postStatusFn: func(_ context.Context, status map[string]any) error {
			sent = status
			return nil
		},
	}
	fa.state.SetControllerStatus(models.ControllerStatusOK)
	fa.state.SetControllerVerified(true)

	fa.PostStatusHelper()
	if sent == nil {
		t.Fatal("expected status payload to be posted")
	}
	raw, ok := sent["knowledgeStatus"].(string)
	if !ok || raw != "[]" {
		t.Fatalf("expected knowledgeStatus \"[]\", got %#v", sent["knowledgeStatus"])
	}
	if sent["activeKnowledge"] != 0 {
		t.Fatalf("expected activeKnowledge 0, got %#v", sent["activeKnowledge"])
	}
	if sent["knowledgeLastUpdate"] != int64(0) {
		t.Fatalf("expected knowledgeLastUpdate 0, got %#v", sent["knowledgeLastUpdate"])
	}
}

func TestControllerMicroservice_LocalOnlyKnowledgeNotBound(t *testing.T) {
	openFieldAgentTestDB(t)
	localOnly := &models.LocalKnowledge{
		Name:       "operator-docs",
		Source:     models.KnowledgeSourceLocal,
		Repo:       "org/local",
		RegistryID: 1,
		State:      models.KnowledgeStateReady,
	}
	localOnly.NormalizeDefaults()
	if err := store.GetInstance().UpsertLocalKnowledge(localOnly); err != nil {
		t.Fatalf("upsert local-only: %v", err)
	}

	ms, err := parseMicroservice(map[string]any{
		"uuid":    "ms-fleet",
		"imageId": "alpine:3.19",
		"knowledge": map[string]any{
			"bindPath": "/knowledge",
			"items":    []any{map[string]any{"name": "operator-docs"}},
		},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	res, prepErr := knowledgecatalog.Prepare(t.TempDir(), store.GetInstance(), ms, models.KnowledgeSourceManaged, true)
	if prepErr != nil {
		t.Fatalf("prepare: %v", prepErr)
	}
	if res.Decision != models.CatalogGateFail {
		t.Fatalf("controller microservice must not bind local-only knowledge, got %v msg=%q", res.Decision, res.Message)
	}
	n, err := store.GetInstance().CountKnowledgeRefs("operator-docs")
	if err != nil {
		t.Fatalf("refs: %v", err)
	}
	if n != 0 {
		t.Fatalf("local-only knowledge must not be bound, got %d refs", n)
	}
}

func TestLoadInitialControllerData_LoadsKnowledgeBeforeMicroservices(t *testing.T) {
	var order []string
	fa := newKnowledgeFieldAgent(t, func(w http.ResponseWriter, r *http.Request) {
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
		case strings.HasSuffix(r.URL.Path, "/agent/knowledge"):
			order = append(order, "knowledge")
			_, _ = w.Write(fixtureControllerKnowledgeJSON())
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

	knowledgeAt, msAt := -1, -1
	for i, name := range order {
		if name == "knowledge" {
			knowledgeAt = i
		}
		if name == "microservices" {
			msAt = i
		}
	}
	if knowledgeAt < 0 || msAt < 0 || knowledgeAt > msAt {
		t.Fatalf("expected knowledge before microservices, order=%v", order)
	}
}
