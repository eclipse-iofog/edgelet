package runtimeapi

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/knowledgemanager"
	"github.com/eclipse-iofog/edgelet/internal/modelpull"
	"github.com/eclipse-iofog/edgelet/internal/models"
)

func TestFacadeApplyLocalKnowledgeManifests_WritesRow(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })
	if err := f.db.EnsureDefaultLocalRegistries(); err != nil {
		t.Fatalf("seed registries: %v", err)
	}
	hf := models.NewRegistryBuilder().SetID(5).SetURL("https://huggingface.co").SetType(models.RegistryTypeHF).Build()
	if err := f.db.UpsertLocalRegistry(hf); err != nil {
		t.Fatalf("upsert registry: %v", err)
	}

	manifest := strings.TrimSpace(`
apiVersion: edgelet.iofog.org/v1
kind: Knowledge
metadata:
  name: product-docs
spec:
  repo: acme/product-manuals
  revision: 9f3c111122223333444455556666777788889999
  registry: 5
  files:
    - data/**/*.jsonl
  format: jsonl
`) + "\n"
	rows, err := f.ApplyLocalKnowledgeManifests(manifest, false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "product-docs" {
		t.Fatalf("unexpected apply rows: %+v", rows)
	}
	got, err := f.GetKnowledge("product-docs")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got["state"] != models.KnowledgeStatePending {
		t.Fatalf("expected Pending after apply, got %#v", got)
	}
}

func TestFacadeListGetKnowledge_SourceAndInspectExtras(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })

	local := &models.LocalKnowledge{
		Name:       "operator-docs",
		Source:     models.KnowledgeSourceLocal,
		Repo:       "org/local",
		RegistryID: 1,
		State:      models.KnowledgeStateReady,
	}
	if err := f.db.UpsertLocalKnowledge(local); err != nil {
		t.Fatalf("upsert local: %v", err)
	}
	managed := &models.LocalKnowledge{
		Name:       "fleet-docs",
		Source:     models.KnowledgeSourceManaged,
		Repo:       "org/fleet",
		RegistryID: 5,
		State:      models.KnowledgeStateReady,
	}
	if err := f.db.UpsertLocalKnowledge(managed); err != nil {
		t.Fatalf("upsert managed: %v", err)
	}
	if err := f.db.SaveControllerKnowledge([]*models.ControllerKnowledge{{
		UUID:       "3f2c8a1e-2b64-4c0d-9f11-0a1b2c3d4e5f",
		Name:       "fleet-docs",
		Repo:       "org/fleet",
		RegistryID: 5,
	}}); err != nil {
		t.Fatalf("save controller knowledge: %v", err)
	}
	if err := f.db.InsertKnowledgeRefs("ms-1", []string{"fleet-docs"}); err != nil {
		t.Fatalf("bind ref: %v", err)
	}

	items, err := f.ListKnowledge()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	sources := map[string]string{}
	for _, item := range items {
		name, ok := item["name"].(string)
		if !ok {
			t.Fatalf("expected name string, got %#v", item["name"])
		}
		source, ok := item["source"].(string)
		if !ok {
			t.Fatalf("expected source string, got %#v", item["source"])
		}
		sources[name] = source
	}
	if sources["operator-docs"] != models.KnowledgeSourceLocal || sources["fleet-docs"] != models.KnowledgeSourceManaged {
		t.Fatalf("expected local and managed sources, got %#v", sources)
	}

	got, err := f.GetKnowledge("fleet-docs")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if got["source"] != models.KnowledgeSourceManaged {
		t.Fatalf("expected managed source, got %#v", got)
	}
	if got["uuid"] != "3f2c8a1e-2b64-4c0d-9f11-0a1b2c3d4e5f" {
		t.Fatalf("expected managed uuid, got %#v", got)
	}
	if got["bindRefCount"] != 1 {
		t.Fatalf("expected bindRefCount 1, got %#v", got)
	}

	localInspect, err := f.GetKnowledge("operator-docs")
	if err != nil {
		t.Fatalf("local inspect: %v", err)
	}
	if localInspect["source"] != models.KnowledgeSourceLocal {
		t.Fatalf("expected local source, got %#v", localInspect)
	}
	if _, ok := localInspect["uuid"]; ok {
		t.Fatalf("local inspect must omit uuid, got %#v", localInspect)
	}
}

func TestFacadeApplyLocalKnowledgeManifests_RejectsManagedName(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })
	if err := f.db.EnsureDefaultLocalRegistries(); err != nil {
		t.Fatalf("seed registries: %v", err)
	}
	hf := models.NewRegistryBuilder().SetID(5).SetURL("https://huggingface.co").SetType(models.RegistryTypeHF).Build()
	if err := f.db.UpsertLocalRegistry(hf); err != nil {
		t.Fatalf("upsert registry: %v", err)
	}
	if err := f.db.SaveControllerKnowledge([]*models.ControllerKnowledge{{
		UUID:       "3f2c1111-2222-3333-4444-555566667777",
		Name:       "fleet-docs",
		Repo:       "org/fleet",
		RegistryID: 5,
	}}); err != nil {
		t.Fatalf("save controller knowledge: %v", err)
	}

	cfg := config.GetInstance()
	origUUID := cfg.IOFogUUID
	cfg.IOFogUUID = "agent-uuid-managed-knowledge"
	t.Cleanup(func() { cfg.IOFogUUID = origUUID })

	_, err := f.ApplyLocalKnowledgeManifests(`
apiVersion: edgelet.iofog.org/v1
kind: Knowledge
metadata:
  name: fleet-docs
spec:
  repo: org/other
  registry: 5
`, false)
	if err == nil || !strings.Contains(err.Error(), "controller-managed") {
		t.Fatalf("expected local apply of managed name to be rejected, got %v", err)
	}
}

func TestFacadeApplyLocalKnowledgeManifests_RefusedWhenWatchdogEnabled(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })
	if err := f.db.EnsureDefaultLocalRegistries(); err != nil {
		t.Fatalf("seed registries: %v", err)
	}

	cfg := config.GetInstance()
	orig := cfg.WatchdogEnabled
	cfg.WatchdogEnabled = true
	t.Cleanup(func() { cfg.WatchdogEnabled = orig })

	_, err := f.ApplyLocalKnowledgeManifests(`
apiVersion: edgelet.iofog.org/v1
kind: Knowledge
metadata:
  name: product-docs
spec:
  repo: org/repo
  registry: 1
`, true)
	if !errors.Is(err, knowledgemanager.ErrLocalKnowledgeDisabled) {
		t.Fatalf("expected local Knowledge apply refused while watchdog is on, got %v", err)
	}
}

func TestFacadePrune_SystemModesLeaveKnowledgeTrees(t *testing.T) {
	f := NewFacade()
	disk := t.TempDir()
	if err := f.db.Open(disk); err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })

	originalDir := f.cfg.DiskDirectory
	f.cfg.DiskDirectory = disk
	t.Cleanup(func() { f.cfg.DiskDirectory = originalDir })

	row := &models.LocalKnowledge{
		Name:       "keep-docs",
		Source:     models.KnowledgeSourceLocal,
		Repo:       "org/keep-docs",
		RegistryID: 1,
		State:      models.KnowledgeStateReady,
	}
	if err := f.db.UpsertLocalKnowledge(row); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	tree := modelpull.ModelDir(modelpull.KnowledgeRoot(disk), "keep-docs")
	if err := os.MkdirAll(filepath.Join(tree, modelpull.ContentDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, modelpull.ManifestFile), []byte(`{"metadataName":"keep-docs"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _ = f.Prune("dangling")
	allResult, err := f.Prune("all")
	if err != nil {
		t.Fatalf("system prune all: %v", err)
	}
	if _, ok := allResult["knowledgeRemoved"]; ok {
		t.Fatalf("system prune must not grow to knowledge, got %#v", allResult)
	}

	if _, err := f.db.GetLocalKnowledge("keep-docs"); err != nil {
		t.Fatalf("knowledge row must remain after system prune: %v", err)
	}
	if _, err := os.Stat(tree); err != nil {
		t.Fatalf("knowledge tree must remain after system prune: %v", err)
	}
}
