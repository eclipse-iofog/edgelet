package processmanager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
)

func TestApplyCatalogStartGate_ThreeStates(t *testing.T) {
	openLocalReconcileTestDB(t)
	t.Cleanup(func() { statusreporter.GetInstance().ResetProcessManagerStatus() })

	disk := t.TempDir()
	content := filepath.Join(disk, "models", "test-model", "content")
	if err := os.MkdirAll(content, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(content, "weights.bin"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	pm := &ProcessManager{
		logger:               logging.NewModuleLogger("test-catalog-gate"),
		catalogDiskDirectory: disk,
	}
	ms := models.NewMicroservice("ms-gate", "nginx:latest")
	ms.Models = &models.ModelCatalog{
		BindPath: "/models",
		Items:    []models.ModelCatalogItem{{Name: "test-model"}},
	}

	upsertCatalogModel(t, "test-model", models.ModelSourceManaged, models.ModelStatePulling, content, 1)
	if pm.applyCatalogStartGate(ms) {
		t.Fatal("pulling must not allow create")
	}
	st := statusreporter.GetInstance().GetProcessManagerStatus().GetMicroserviceStatus("ms-gate")
	if st.Status != models.MicroserviceStateQueued {
		t.Fatalf("expected QUEUED, got %s", st.Status)
	}
	if st.ErrorMessage == nil || !strings.Contains(*st.ErrorMessage, "test-model") || !strings.Contains(*st.ErrorMessage, models.ModelStatePulling) {
		t.Fatalf("expected wait text with model name and state, got %v", st.ErrorMessage)
	}

	upsertCatalogModel(t, "test-model", models.ModelSourceManaged, models.ModelStateFailed, content, 1)
	if pm.applyCatalogStartGate(ms) {
		t.Fatal("Failed must not allow create")
	}
	st = statusreporter.GetInstance().GetProcessManagerStatus().GetMicroserviceStatus("ms-gate")
	if st.Status != models.MicroserviceStateFailed {
		t.Fatalf("expected FAILED, got %s", st.Status)
	}

	upsertCatalogModel(t, "test-model", models.ModelSourceManaged, models.ModelStateReady, content, 2)
	if !pm.applyCatalogStartGate(ms) {
		t.Fatal("Ready must allow create")
	}
}

func TestRefreshCatalogProjection_InPlaceVsBindPath(t *testing.T) {
	openLocalReconcileTestDB(t)
	disk := t.TempDir()
	contentA := filepath.Join(disk, "models", "model-a", "content")
	contentB := filepath.Join(disk, "models", "model-b", "content")
	for _, dir := range []string{contentA, contentB} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "w.bin"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	upsertCatalogModel(t, "model-a", models.ModelSourceManaged, models.ModelStateReady, contentA, 1)
	upsertCatalogModel(t, "model-b", models.ModelSourceManaged, models.ModelStateReady, contentB, 1)

	pm := &ProcessManager{
		logger:               logging.NewModuleLogger("test-catalog-refresh"),
		catalogDiskDirectory: disk,
	}
	ms := models.NewMicroservice("ms-refresh", "nginx:latest")
	ms.Models = &models.ModelCatalog{
		BindPath: "/models",
		Items:    []models.ModelCatalogItem{{Name: "model-a"}},
	}
	if !pm.applyCatalogStartGate(ms) {
		t.Fatal("initial Ready catalog must allow create")
	}

	ms.Models.Items = append(ms.Models.Items, models.ModelCatalogItem{Name: "model-b"})
	pm.refreshCatalogProjection(ms)
	if ms.Rebuild {
		t.Fatal("adding a catalog item must not recreate")
	}

	ms.Models.BindPath = "/weights"
	pm.refreshCatalogProjection(ms)
	if !ms.Rebuild {
		t.Fatal("bindPath change must mark recreate")
	}
}

func TestRefreshCatalogProjection_RemoveLastItemRecreates(t *testing.T) {
	openLocalReconcileTestDB(t)
	disk := t.TempDir()
	content := filepath.Join(disk, "models", "model-a", "content")
	if err := os.MkdirAll(content, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(content, "w.bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	upsertCatalogModel(t, "model-a", models.ModelSourceManaged, models.ModelStateReady, content, 1)

	pm := &ProcessManager{
		logger:               logging.NewModuleLogger("test-catalog-last-item"),
		catalogDiskDirectory: disk,
	}
	ms := models.NewMicroservice("ms-last", "nginx:latest")
	ms.Models = &models.ModelCatalog{
		BindPath: "/models",
		Items:    []models.ModelCatalogItem{{Name: "model-a"}},
	}
	if !pm.applyCatalogStartGate(ms) {
		t.Fatal("initial Ready catalog must allow create")
	}

	ms.Models.Items = nil
	pm.refreshCatalogProjection(ms)
	if !ms.Rebuild {
		t.Fatal("removing the last catalog item must recreate")
	}
	if _, err := os.Stat(filepath.Join(disk, "volumes", "microservices", "ms-last", "models", "model-a", "w.bin")); err != nil {
		t.Fatalf("refresh must keep the previous projection until recreate: %v", err)
	}
}

func TestRefreshCatalogProjection_EmptyToNonemptyRecreates(t *testing.T) {
	openLocalReconcileTestDB(t)
	disk := t.TempDir()
	content := filepath.Join(disk, "models", "model-a", "content")
	if err := os.MkdirAll(content, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(content, "w.bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	upsertCatalogModel(t, "model-a", models.ModelSourceManaged, models.ModelStateReady, content, 1)

	pm := &ProcessManager{
		logger:               logging.NewModuleLogger("test-catalog-empty-add"),
		catalogDiskDirectory: disk,
	}
	ms := models.NewMicroservice("ms-empty-add", "nginx:latest")
	pm.refreshCatalogProjection(ms)
	if ms.Rebuild {
		t.Fatal("no catalog must not recreate")
	}

	ms.Models = &models.ModelCatalog{
		BindPath: "/models",
		Items:    []models.ModelCatalogItem{{Name: "model-a"}},
	}
	pm.refreshCatalogProjection(ms)
	if !ms.Rebuild {
		t.Fatal("empty to nonempty catalog must recreate")
	}
}

func TestRefreshCatalogProjection_RunningWaitKeepsOldProjection(t *testing.T) {
	openLocalReconcileTestDB(t)
	t.Cleanup(func() { statusreporter.GetInstance().ResetProcessManagerStatus() })

	disk := t.TempDir()
	contentA := filepath.Join(disk, "models", "model-a", "content")
	contentB := filepath.Join(disk, "models", "model-b", "content")
	for _, dir := range []string{contentA, contentB} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "w.bin"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	upsertCatalogModel(t, "model-a", models.ModelSourceManaged, models.ModelStateReady, contentA, 1)
	upsertCatalogModel(t, "model-b", models.ModelSourceManaged, models.ModelStatePulling, contentB, 1)

	pm := &ProcessManager{
		logger:               logging.NewModuleLogger("test-catalog-running-wait"),
		catalogDiskDirectory: disk,
	}
	ms := models.NewMicroservice("ms-running-wait", "nginx:latest")
	ms.Models = &models.ModelCatalog{
		BindPath: "/models",
		Items:    []models.ModelCatalogItem{{Name: "model-a"}},
	}
	if !pm.applyCatalogStartGate(ms) {
		t.Fatal("initial Ready catalog must allow create")
	}
	statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
		s.SetMicroservicesState(ms.MicroserviceUUID, models.MicroserviceStateRunning)
	})
	hostDir := filepath.Join(disk, "volumes", "microservices", ms.MicroserviceUUID, "models")
	before, err := os.ReadFile(filepath.Join(hostDir, "model-a", "w.bin")) // #nosec G304 -- test fixture
	if err != nil {
		t.Fatal(err)
	}

	ms.Models.Items = append(ms.Models.Items, models.ModelCatalogItem{Name: "model-b"})
	pm.refreshCatalogProjection(ms)
	if ms.Rebuild {
		t.Fatal("adding a pending catalog item must not recreate a running workload")
	}
	st := statusreporter.GetInstance().GetProcessManagerStatus().GetMicroserviceStatus(ms.MicroserviceUUID)
	if st == nil || st.Status != models.MicroserviceStateRunning {
		t.Fatalf("running workload must not move to QUEUED for a pending item, got %+v", st)
	}
	got, err := os.ReadFile(filepath.Join(hostDir, "model-a", "w.bin")) // #nosec G304 -- test fixture
	if err != nil {
		t.Fatalf("previous projection must stay until Ready: %v", err)
	}
	if string(got) != string(before) {
		t.Fatal("previous projection bytes changed before Ready")
	}
	if _, err := os.Lstat(filepath.Join(hostDir, "model-b")); !os.IsNotExist(err) {
		t.Fatal("must not project the pending name until Ready")
	}

	upsertCatalogModel(t, "model-b", models.ModelSourceManaged, models.ModelStateReady, contentB, 1)
	pm.refreshCatalogProjection(ms)
	if ms.Rebuild {
		t.Fatal("Ready item add must stay in-place")
	}
	if _, err := os.ReadFile(filepath.Join(hostDir, "model-b", "w.bin")); err != nil { // #nosec G304 -- test fixture
		t.Fatalf("Ready item must be projected: %v", err)
	}
	st = statusreporter.GetInstance().GetProcessManagerStatus().GetMicroserviceStatus(ms.MicroserviceUUID)
	if st == nil || st.Status != models.MicroserviceStateRunning {
		t.Fatalf("in-place swing must keep running, got %+v", st)
	}
}

func TestApplyCatalogStartGate_KnowledgeConjunction(t *testing.T) {
	openLocalReconcileTestDB(t)
	t.Cleanup(func() { statusreporter.GetInstance().ResetProcessManagerStatus() })

	disk := t.TempDir()
	modelContent := filepath.Join(disk, "models", "test-model", "content")
	knowledgeContent := filepath.Join(disk, "knowledge", "product-docs", "content")
	for _, dir := range []string{modelContent, knowledgeContent} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "payload.bin"), []byte("ok"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	pm := &ProcessManager{
		logger:               logging.NewModuleLogger("test-catalog-conjunction"),
		catalogDiskDirectory: disk,
	}
	ms := models.NewMicroservice("ms-both", "nginx:latest")
	ms.Models = &models.ModelCatalog{
		BindPath: "/models",
		Items:    []models.ModelCatalogItem{{Name: "test-model"}},
	}
	ms.Knowledge = &models.KnowledgeCatalog{
		BindPath: "/knowledge",
		Items:    []models.KnowledgeCatalogItem{{Name: "product-docs"}},
	}

	upsertCatalogModel(t, "test-model", models.ModelSourceManaged, models.ModelStateReady, modelContent, 1)
	upsertCatalogKnowledge(t, "product-docs", models.KnowledgeSourceManaged, models.KnowledgeStatePulling, 1)
	if pm.applyCatalogStartGate(ms) {
		t.Fatal("models Ready + knowledge Pulling must not allow create")
	}
	st := statusreporter.GetInstance().GetProcessManagerStatus().GetMicroserviceStatus("ms-both")
	if st.Status != models.MicroserviceStateQueued {
		t.Fatalf("expected QUEUED, got %s", st.Status)
	}
	if st.ErrorMessage == nil || !strings.Contains(*st.ErrorMessage, "product-docs") || !strings.Contains(*st.ErrorMessage, models.KnowledgeStatePulling) {
		t.Fatalf("expected knowledge wait text, got %v", st.ErrorMessage)
	}

	upsertCatalogKnowledge(t, "product-docs", models.KnowledgeSourceManaged, models.KnowledgeStateReady, 1)
	if !pm.applyCatalogStartGate(ms) {
		t.Fatal("both catalogs Ready must allow create")
	}
}

func TestRefreshCatalogProjection_KnowledgeInPlaceVsBindPath(t *testing.T) {
	openLocalReconcileTestDB(t)
	disk := t.TempDir()
	contentA := filepath.Join(disk, "knowledge", "docs-a", "content")
	contentB := filepath.Join(disk, "knowledge", "docs-b", "content")
	for _, dir := range []string{contentA, contentB} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "guide.md"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	upsertCatalogKnowledge(t, "docs-a", models.KnowledgeSourceManaged, models.KnowledgeStateReady, 1)
	upsertCatalogKnowledge(t, "docs-b", models.KnowledgeSourceManaged, models.KnowledgeStateReady, 1)

	pm := &ProcessManager{
		logger:               logging.NewModuleLogger("test-knowledge-refresh"),
		catalogDiskDirectory: disk,
	}
	ms := models.NewMicroservice("ms-knowledge-refresh", "nginx:latest")
	ms.Knowledge = &models.KnowledgeCatalog{
		BindPath: "/knowledge",
		Items:    []models.KnowledgeCatalogItem{{Name: "docs-a"}},
	}
	if !pm.applyCatalogStartGate(ms) {
		t.Fatal("initial Ready knowledge catalog must allow create")
	}

	ms.Knowledge.Items = append(ms.Knowledge.Items, models.KnowledgeCatalogItem{Name: "docs-b"})
	pm.refreshCatalogProjection(ms)
	if ms.Rebuild {
		t.Fatal("adding a knowledge catalog item must not recreate")
	}

	ms.Knowledge.BindPath = "/corpus"
	pm.refreshCatalogProjection(ms)
	if !ms.Rebuild {
		t.Fatal("knowledge bindPath change must mark recreate")
	}
}

func upsertCatalogKnowledge(t *testing.T, name, source, state string, generation int64) {
	t.Helper()
	row := &models.LocalKnowledge{
		Name:       name,
		Source:     source,
		Repo:       "org/docs",
		RegistryID: 1,
		State:      state,
		Generation: generation,
	}
	row.NormalizeDefaults()
	row.Source = source
	row.State = state
	row.Generation = generation
	if err := store.GetInstance().UpsertLocalKnowledge(row); err != nil {
		t.Fatalf("upsert knowledge %s: %v", name, err)
	}
}

func upsertCatalogModel(t *testing.T, name, source, state, content string, generation int64) {
	t.Helper()
	row := &models.LocalModel{
		Name:        name,
		Source:      source,
		Repo:        "org/repo",
		RegistryID:  1,
		State:       state,
		ContentPath: content,
		Generation:  generation,
	}
	row.NormalizeDefaults()
	row.Source = source
	row.State = state
	row.Generation = generation
	if err := store.GetInstance().UpsertLocalModel(row); err != nil {
		t.Fatalf("upsert %s: %v", name, err)
	}
}
