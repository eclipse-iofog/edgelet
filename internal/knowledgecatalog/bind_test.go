package knowledgecatalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/knowledgemanager"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/store"
)

func TestPrepare_StartGateStatesAndRefs(t *testing.T) {
	db := openCatalogTestDB(t)
	disk := t.TempDir()
	content := filepath.Join(disk, "knowledge", "product-docs", "content")
	if err := os.MkdirAll(content, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(content, "guide.md"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	ms := models.NewMicroservice("ms-gate", "nginx:latest")
	ms.Knowledge = &models.KnowledgeCatalog{
		BindPath: "/knowledge",
		Items:    []models.KnowledgeCatalogItem{{Name: "product-docs"}},
	}

	upsertKnowledge(t, db, "product-docs", models.KnowledgeSourceLocal, models.KnowledgeStatePulling, 1)
	waiting, err := Prepare(disk, db, ms, models.KnowledgeSourceLocal, true)
	if err != nil {
		t.Fatalf("prepare pulling: %v", err)
	}
	if waiting.Decision != models.CatalogGateWait {
		t.Fatalf("expected wait, got %v", waiting.Decision)
	}
	if !strings.Contains(waiting.Message, "product-docs") || !strings.Contains(waiting.Message, models.KnowledgeStatePulling) {
		t.Fatalf("expected wait text with knowledge name and state, got %q", waiting.Message)
	}
	assertRefCount(t, db, "product-docs", 1)
	if _, err := os.Stat(filepath.Join(waiting.HostDir, "product-docs")); !os.IsNotExist(err) {
		t.Fatal("must not project until Ready")
	}

	upsertKnowledge(t, db, "product-docs", models.KnowledgeSourceLocal, models.KnowledgeStateFailed, 1)
	row, err := db.GetLocalKnowledge("product-docs")
	if err != nil {
		t.Fatal(err)
	}
	row.LastError = "download failed"
	if err := db.UpsertLocalKnowledge(row); err != nil {
		t.Fatal(err)
	}
	failed, err := Prepare(disk, db, ms, models.KnowledgeSourceLocal, true)
	if err != nil {
		t.Fatalf("prepare failed: %v", err)
	}
	if failed.Decision != models.CatalogGateFail {
		t.Fatalf("expected fail, got %v", failed.Decision)
	}

	upsertKnowledge(t, db, "product-docs", models.KnowledgeSourceLocal, models.KnowledgeStateReady, 2)
	ready, err := Prepare(disk, db, ms, models.KnowledgeSourceLocal, true)
	if err != nil {
		t.Fatalf("prepare ready: %v", err)
	}
	if ready.Decision != models.CatalogGateAllow {
		t.Fatalf("expected allow, got %v msg=%q", ready.Decision, ready.Message)
	}
	if _, err := os.ReadFile(filepath.Join(ready.HostDir, "product-docs", "guide.md")); err != nil { // #nosec G304 -- test fixture
		t.Fatalf("expected projected content: %v", err)
	}

	if err := Release(disk, db, ms.MicroserviceUUID); err != nil {
		t.Fatalf("release: %v", err)
	}
	assertRefCount(t, db, "product-docs", 0)
}

func TestPrepare_ControllerMustNotBindLocalOnly(t *testing.T) {
	db := openCatalogTestDB(t)
	disk := t.TempDir()
	upsertKnowledge(t, db, "operator-docs", models.KnowledgeSourceLocal, models.KnowledgeStateReady, 1)

	ms := models.NewMicroservice("ms-fleet", "nginx:latest")
	ms.Knowledge = &models.KnowledgeCatalog{
		BindPath: "/knowledge",
		Items:    []models.KnowledgeCatalogItem{{Name: "operator-docs"}},
	}
	res, err := Prepare(disk, db, ms, models.KnowledgeSourceManaged, true)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if res.Decision != models.CatalogGateFail {
		t.Fatalf("controller workload must not bind local-only names, got %v", res.Decision)
	}
	assertRefCount(t, db, "operator-docs", 0)
}

func TestPrepare_SourceScopedBind(t *testing.T) {
	db := openCatalogTestDB(t)
	disk := t.TempDir()
	upsertKnowledge(t, db, "fleet-docs", models.KnowledgeSourceManaged, models.KnowledgeStateReady, 1)

	ms := models.NewMicroservice("ms-local", "nginx:latest")
	ms.Knowledge = &models.KnowledgeCatalog{
		BindPath: "/knowledge",
		Items:    []models.KnowledgeCatalogItem{{Name: "fleet-docs"}},
	}
	res, err := Prepare(disk, db, ms, models.KnowledgeSourceLocal, true)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if res.Decision != models.CatalogGateFail {
		t.Fatalf("local workload must not bind managed names, got %v", res.Decision)
	}
	assertRefCount(t, db, "fleet-docs", 0)
}

func TestPrepare_RefreshKeepsProjectionUntilReady(t *testing.T) {
	db := openCatalogTestDB(t)
	disk := t.TempDir()
	contentA := filepath.Join(disk, "knowledge", "docs-a", "content")
	contentB := filepath.Join(disk, "knowledge", "docs-b", "content")
	for _, dir := range []string{contentA, contentB} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "guide.md"), []byte("ok"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	upsertKnowledge(t, db, "docs-a", models.KnowledgeSourceManaged, models.KnowledgeStateReady, 1)
	upsertKnowledge(t, db, "docs-b", models.KnowledgeSourceManaged, models.KnowledgeStatePulling, 1)

	ms := models.NewMicroservice("ms-wait", "nginx:latest")
	ms.Knowledge = &models.KnowledgeCatalog{
		BindPath: "/knowledge",
		Items:    []models.KnowledgeCatalogItem{{Name: "docs-a"}},
	}
	first, err := Prepare(disk, db, ms, models.KnowledgeSourceManaged, true)
	if err != nil || first.Decision != models.CatalogGateAllow {
		t.Fatalf("initial project: decision=%v err=%v", first.Decision, err)
	}
	before, err := os.ReadFile(filepath.Join(first.HostDir, "docs-a", "guide.md")) // #nosec G304 -- test fixture
	if err != nil {
		t.Fatal(err)
	}

	ms.Knowledge.Items = append(ms.Knowledge.Items, models.KnowledgeCatalogItem{Name: "docs-b"})
	waiting, err := Prepare(disk, db, ms, models.KnowledgeSourceManaged, false)
	if err != nil {
		t.Fatalf("refresh while pulling: %v", err)
	}
	if waiting.Decision != models.CatalogGateWait {
		t.Fatalf("expected wait, got %v", waiting.Decision)
	}
	if waiting.MountChanged {
		t.Fatal("adding an item must not report a mount change")
	}
	got, err := os.ReadFile(filepath.Join(first.HostDir, "docs-a", "guide.md")) // #nosec G304 -- test fixture
	if err != nil {
		t.Fatalf("previous projection must stay until Ready: %v", err)
	}
	if string(got) != string(before) {
		t.Fatal("previous projection bytes changed before Ready")
	}
	if _, err := os.Lstat(filepath.Join(first.HostDir, "docs-b")); !os.IsNotExist(err) {
		t.Fatal("must not project the pending name until Ready")
	}

	upsertKnowledge(t, db, "docs-b", models.KnowledgeSourceManaged, models.KnowledgeStateReady, 1)
	ready, err := Prepare(disk, db, ms, models.KnowledgeSourceManaged, false)
	if err != nil || ready.Decision != models.CatalogGateAllow {
		t.Fatalf("refresh after Ready: decision=%v err=%v", ready.Decision, err)
	}
	if ready.MountChanged {
		t.Fatal("in-place item add must not report a mount change")
	}
	if _, err := os.ReadFile(filepath.Join(ready.HostDir, "docs-b", "guide.md")); err != nil { // #nosec G304 -- test fixture
		t.Fatalf("Ready item must be projected: %v", err)
	}
}

func TestPrepare_RemoveRefusedWhileBound(t *testing.T) {
	db := openCatalogTestDB(t)
	disk := t.TempDir()
	content := filepath.Join(disk, "knowledge", "product-docs", "content")
	if err := os.MkdirAll(content, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(content, "guide.md"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	upsertKnowledge(t, db, "product-docs", models.KnowledgeSourceLocal, models.KnowledgeStateReady, 1)

	ms := models.NewMicroservice("ms-bound", "nginx:latest")
	ms.Knowledge = &models.KnowledgeCatalog{
		BindPath: "/knowledge",
		Items:    []models.KnowledgeCatalogItem{{Name: "product-docs"}},
	}
	if _, err := Prepare(disk, db, ms, models.KnowledgeSourceLocal, true); err != nil {
		t.Fatal(err)
	}
	assertRefCount(t, db, "product-docs", 1)

	mgr := knowledgemanager.New(db, filepath.Join(disk, "knowledge"))
	err := mgr.Remove("product-docs")
	if err == nil || !strings.Contains(err.Error(), "bound") {
		t.Fatalf("expected remove refused while bound, got %v", err)
	}
}

func openCatalogTestDB(t *testing.T) *store.DB {
	t.Helper()
	db := store.GetInstance()
	_ = db.Close()
	if err := db.Open(t.TempDir()); err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func upsertKnowledge(t *testing.T, db *store.DB, name, source, state string, generation int64) {
	t.Helper()
	row := &models.LocalKnowledge{
		Name:       name,
		Source:     source,
		Repo:       "org/docs",
		RegistryID: 1,
		State:      state,
		Generation: generation,
		LastError:  "",
	}
	row.NormalizeDefaults()
	row.State = state
	row.Source = source
	row.Generation = generation
	if err := db.UpsertLocalKnowledge(row); err != nil {
		t.Fatalf("upsert knowledge %s: %v", name, err)
	}
}

func assertRefCount(t *testing.T, db *store.DB, name string, want int) {
	t.Helper()
	n, err := db.CountKnowledgeRefs(name)
	if err != nil {
		t.Fatal(err)
	}
	if n != want {
		t.Fatalf("ref count for %s: got %d want %d", name, n, want)
	}
}
