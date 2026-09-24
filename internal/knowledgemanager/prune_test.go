package knowledgemanager

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/modelpull"
	"github.com/eclipse-iofog/edgelet/internal/modelpull/ocistore"
	"github.com/eclipse-iofog/edgelet/internal/models"
)

func plantKnowledgeTree(t *testing.T, root, name string) string {
	t.Helper()
	dir := modelpull.ModelDir(root, name)
	if err := os.MkdirAll(filepath.Join(dir, modelpull.ContentDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, modelpull.ManifestFile), []byte(`{"metadataName":"`+name+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func upsertReadyLocalKnowledge(t *testing.T, m *Manager, name, source string) {
	t.Helper()
	row := sampleKnowledge(name, "org/"+name, "latest", 1, nil)
	row.Source = source
	row.State = models.KnowledgeStateReady
	if err := m.db.UpsertLocalKnowledge(row); err != nil {
		t.Fatalf("upsert %s: %v", name, err)
	}
	plantKnowledgeTree(t, m.knowledgeRoot, name)
}

func TestPruneDangling_RemovesUnboundLocalRowAndTree(t *testing.T) {
	m, _, _ := newTestManager(t)
	upsertReadyLocalKnowledge(t, m, "unbound-local", models.KnowledgeSourceLocal)

	report, err := m.PruneDangling()
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(report.Removed) != 1 || report.Removed[0] != "unbound-local" {
		t.Fatalf("expected unbound-local removed, got %+v", report)
	}
	if _, err := m.db.GetLocalKnowledge("unbound-local"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected local row removed, err=%v", err)
	}
	if _, err := os.Stat(modelpull.ModelDir(m.knowledgeRoot, "unbound-local")); !os.IsNotExist(err) {
		t.Fatal("expected on-disk tree removed")
	}
}

func TestPruneDangling_KeepsBoundLocalKnowledge(t *testing.T) {
	m, _, _ := newTestManager(t)
	upsertReadyLocalKnowledge(t, m, "bound-local", models.KnowledgeSourceLocal)
	if err := m.db.ReplaceKnowledgeRefs("ms-1", []string{"bound-local"}); err != nil {
		t.Fatalf("bind: %v", err)
	}

	report, err := m.PruneDangling()
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	for _, name := range report.Removed {
		if name == "bound-local" {
			t.Fatalf("bound local knowledge must be kept, got %+v", report)
		}
	}
	if _, err := m.db.GetLocalKnowledge("bound-local"); err != nil {
		t.Fatalf("bound row must remain: %v", err)
	}
	if _, err := os.Stat(modelpull.ModelDir(m.knowledgeRoot, "bound-local")); err != nil {
		t.Fatalf("bound tree must remain: %v", err)
	}
}

func TestPruneDangling_KeepsUnboundManagedKnowledge(t *testing.T) {
	m, _, _ := newTestManager(t)
	upsertReadyLocalKnowledge(t, m, "fleet-unbound", models.KnowledgeSourceManaged)
	if err := m.db.SaveControllerKnowledge([]*models.ControllerKnowledge{{
		UUID:       "3f2c8a1e-2b64-4c0d-9f11-0a1b2c3d4e5f",
		Name:       "fleet-unbound",
		Repo:       "org/fleet-unbound",
		RegistryID: 1,
	}}); err != nil {
		t.Fatalf("save controller knowledge: %v", err)
	}

	report, err := m.PruneDangling()
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	for _, name := range report.Removed {
		if name == "fleet-unbound" {
			t.Fatalf("unbound managed knowledge must be kept, got %+v", report)
		}
	}
	if _, err := m.db.GetLocalKnowledge("fleet-unbound"); err != nil {
		t.Fatalf("managed row must remain: %v", err)
	}
	if _, err := os.Stat(modelpull.ModelDir(m.knowledgeRoot, "fleet-unbound")); err != nil {
		t.Fatalf("managed tree must remain: %v", err)
	}
}

func TestPruneDangling_SkipsActivePull(t *testing.T) {
	m, ociPuller, _ := newTestManager(t)
	if _, err := m.UpsertDesired(sampleKnowledge("pulling-docs", "acme/pulling", "latest", 1, nil)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	ociPuller.block = make(chan struct{})
	op, err := m.StartPull("pulling-docs")
	if err != nil {
		t.Fatalf("start pull: %v", err)
	}

	report, err := m.PruneDangling()
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	for _, name := range report.Removed {
		if name == "pulling-docs" {
			t.Fatalf("active pull must not be deleted, got %+v", report)
		}
	}
	if _, err := m.db.GetLocalKnowledge("pulling-docs"); err != nil {
		t.Fatalf("pulling row must remain: %v", err)
	}
	if op == nil || op.Name != "pulling-docs" {
		t.Fatalf("expected running pull, got %+v", op)
	}
	close(ociPuller.block)
	waitOp(t, m, op.OperationID)
}

func TestPruneDangling_KeepsSharedReferencedBlob(t *testing.T) {
	m, _, _ := newTestManager(t)
	payload := []byte("shared-corpus")
	sum := sha256.Sum256(payload)
	digest := "sha256:" + hex.EncodeToString(sum[:])

	store, err := ocistore.Open(modelpull.OCIStoreDir(m.knowledgeRoot))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.WriteBlob(digest, bytes.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatalf("write blob: %v", err)
	}

	for _, name := range []string{"shared-a", "shared-b"} {
		upsertReadyLocalKnowledge(t, m, name, models.KnowledgeSourceLocal)
		if err := modelpull.WriteOnDiskManifest(modelpull.ManifestPath(m.knowledgeRoot, name), modelpull.OnDiskManifest{
			MetadataName: name,
			Digest:       digest,
			Blobs:        []string{digest},
		}); err != nil {
			t.Fatalf("write manifest %s: %v", name, err)
		}
		if err := store.Track(name, digest, []string{digest}); err != nil {
			t.Fatalf("track %s: %v", name, err)
		}
	}
	if err := m.db.ReplaceKnowledgeRefs("ms-1", []string{"shared-b"}); err != nil {
		t.Fatalf("bind: %v", err)
	}

	report, err := m.PruneDangling()
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	foundA := false
	for _, name := range report.Removed {
		if name == "shared-a" {
			foundA = true
		}
		if name == "shared-b" {
			t.Fatalf("referenced knowledge must be kept, got %+v", report)
		}
	}
	if !foundA {
		t.Fatalf("expected unused local knowledge removed, got %+v", report)
	}
	has, err := store.HasBlob(digest)
	if err != nil || !has {
		t.Fatalf("shared blob must remain while still referenced, has=%v err=%v", has, err)
	}
}

func TestPruneDangling_WatchdogRemovesLocalKeepsManaged(t *testing.T) {
	m, _, _ := newTestManager(t)
	upsertReadyLocalKnowledge(t, m, "bound-local", models.KnowledgeSourceLocal)
	if err := m.db.ReplaceKnowledgeRefs("ms-1", []string{"bound-local"}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	upsertReadyLocalKnowledge(t, m, "fleet-kept", models.KnowledgeSourceManaged)
	if err := m.db.SaveControllerKnowledge([]*models.ControllerKnowledge{{
		UUID:       "3f2c8a1e-2b64-4c0d-9f11-0a1b2c3d4e5f",
		Name:       "fleet-kept",
		Repo:       "org/fleet-kept",
		RegistryID: 1,
	}}); err != nil {
		t.Fatalf("save controller knowledge: %v", err)
	}

	cfg := config.GetInstance()
	orig := cfg.WatchdogEnabled
	cfg.WatchdogEnabled = true
	t.Cleanup(func() { cfg.WatchdogEnabled = orig })

	report, err := m.PruneDangling()
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	foundLocal := false
	for _, name := range report.Removed {
		if name == "bound-local" {
			foundLocal = true
		}
		if name == "fleet-kept" {
			t.Fatalf("watchdog must not delete managed trees, got %+v", report)
		}
	}
	if !foundLocal {
		t.Fatalf("expected bound local knowledge removed under watchdog, got %+v", report)
	}
	if _, err := m.db.GetLocalKnowledge("bound-local"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected local row removed, err=%v", err)
	}
	if _, err := m.db.GetLocalKnowledge("fleet-kept"); err != nil {
		t.Fatalf("managed row must remain: %v", err)
	}
}

func TestApplyManifest_RefusedWhenWatchdogEnabled(t *testing.T) {
	m, _, _ := newTestManager(t)
	cfg := config.GetInstance()
	orig := cfg.WatchdogEnabled
	cfg.WatchdogEnabled = true
	t.Cleanup(func() { cfg.WatchdogEnabled = orig })

	doc := &models.LocalKnowledgeManifest{
		APIVersion: "edgelet.iofog.org/v1",
		Kind:       "Knowledge",
		Spec: models.LocalKnowledgeSpec{
			Repo:     "org/repo",
			Registry: 1,
		},
	}
	doc.Metadata.Name = "product-docs"
	_, err := m.ApplyManifest(doc)
	if !errors.Is(err, ErrLocalKnowledgeDisabled) {
		t.Fatalf("expected local knowledge disabled, got %v", err)
	}
}

func TestStartPull_RefusedForLocalSourceWhenWatchdogEnabled(t *testing.T) {
	m, _, _ := newTestManager(t)
	if _, err := m.UpsertDesired(sampleKnowledge("local-pull", "org/local-pull", "latest", 1, nil)); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	cfg := config.GetInstance()
	orig := cfg.WatchdogEnabled
	cfg.WatchdogEnabled = true
	t.Cleanup(func() { cfg.WatchdogEnabled = orig })

	_, err := m.StartPull("local-pull")
	if !errors.Is(err, ErrLocalKnowledgeDisabled) {
		t.Fatalf("expected local pull refused, got %v", err)
	}
}
