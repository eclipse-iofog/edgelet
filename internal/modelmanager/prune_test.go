package modelmanager

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/modelpull"
	"github.com/eclipse-iofog/edgelet/internal/models"
)

func plantModelTree(t *testing.T, root, name string) string {
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

func upsertReadyLocal(t *testing.T, m *Manager, name, source string) {
	t.Helper()
	row := sampleModel(name, "org/"+name, "latest", 1, nil)
	row.Source = source
	row.State = models.ModelStateReady
	if err := m.db.UpsertLocalModel(row); err != nil {
		t.Fatalf("upsert %s: %v", name, err)
	}
	plantModelTree(t, m.modelsRoot, name)
}

func TestPruneDangling_RemovesUnboundLocalRowAndTree(t *testing.T) {
	m, _, _ := newTestManager(t)
	upsertReadyLocal(t, m, "unbound-local", models.ModelSourceLocal)

	report, err := m.PruneDangling()
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(report.Removed) != 1 || report.Removed[0] != "unbound-local" {
		t.Fatalf("expected unbound-local removed, got %+v", report)
	}
	if _, err := m.db.GetLocalModel("unbound-local"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected local row removed, err=%v", err)
	}
	if _, err := os.Stat(modelpull.ModelDir(m.modelsRoot, "unbound-local")); !os.IsNotExist(err) {
		t.Fatal("expected on-disk tree removed")
	}
}

func TestPruneDangling_KeepsBoundLocalModel(t *testing.T) {
	m, _, _ := newTestManager(t)
	upsertReadyLocal(t, m, "bound-local", models.ModelSourceLocal)
	if err := m.db.ReplaceWorkloadModelRefs("ms-1", []string{"bound-local"}); err != nil {
		t.Fatalf("bind: %v", err)
	}

	report, err := m.PruneDangling()
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	for _, name := range report.Removed {
		if name == "bound-local" {
			t.Fatalf("bound local model must be kept, got %+v", report)
		}
	}
	if _, err := m.db.GetLocalModel("bound-local"); err != nil {
		t.Fatalf("bound row must remain: %v", err)
	}
	if _, err := os.Stat(modelpull.ModelDir(m.modelsRoot, "bound-local")); err != nil {
		t.Fatalf("bound tree must remain: %v", err)
	}
}

func TestPruneDangling_KeepsUnboundManagedModel(t *testing.T) {
	m, _, _ := newTestManager(t)
	upsertReadyLocal(t, m, "fleet-unbound", models.ModelSourceManaged)
	if err := m.db.SaveControllerModels([]*models.ControllerModel{{
		UUID:       "3f2c8a1e-2b64-4c0d-9f11-0a1b2c3d4e5f",
		Name:       "fleet-unbound",
		Repo:       "org/fleet-unbound",
		RegistryID: 1,
	}}); err != nil {
		t.Fatalf("save controller model: %v", err)
	}

	report, err := m.PruneDangling()
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	for _, name := range report.Removed {
		if name == "fleet-unbound" {
			t.Fatalf("unbound managed model must be kept, got %+v", report)
		}
	}
	if _, err := m.db.GetLocalModel("fleet-unbound"); err != nil {
		t.Fatalf("managed row must remain: %v", err)
	}
	if _, err := os.Stat(modelpull.ModelDir(m.modelsRoot, "fleet-unbound")); err != nil {
		t.Fatalf("managed tree must remain: %v", err)
	}
}

func TestPruneDangling_RemovesOnDiskNameNotInKeepSet(t *testing.T) {
	m, _, _ := newTestManager(t)
	orphan := plantModelTree(t, m.modelsRoot, "leftover")

	report, err := m.PruneDangling()
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(report.Removed) != 1 || report.Removed[0] != "leftover" {
		t.Fatalf("expected leftover removed, got %+v", report)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("expected leftover directory removed")
	}
}

func TestPruneDangling_WatchdogRemovesAllLocalModels(t *testing.T) {
	m, _, _ := newTestManager(t)
	upsertReadyLocal(t, m, "bound-local", models.ModelSourceLocal)
	if err := m.db.ReplaceWorkloadModelRefs("ms-1", []string{"bound-local"}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	upsertReadyLocal(t, m, "fleet-kept", models.ModelSourceManaged)
	if err := m.db.SaveControllerModels([]*models.ControllerModel{{
		UUID:       "3f2c8a1e-2b64-4c0d-9f11-0a1b2c3d4e5f",
		Name:       "fleet-kept",
		Repo:       "org/fleet-kept",
		RegistryID: 1,
	}}); err != nil {
		t.Fatalf("save controller model: %v", err)
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
		t.Fatalf("expected bound local model removed under watchdog, got %+v", report)
	}
	if _, err := m.db.GetLocalModel("bound-local"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected local row removed, err=%v", err)
	}
	if _, err := m.db.GetLocalModel("fleet-kept"); err != nil {
		t.Fatalf("managed row must remain: %v", err)
	}
}

func TestApplyManifest_RefusedWhenWatchdogEnabled(t *testing.T) {
	m, _, _ := newTestManager(t)
	cfg := config.GetInstance()
	orig := cfg.WatchdogEnabled
	cfg.WatchdogEnabled = true
	t.Cleanup(func() { cfg.WatchdogEnabled = orig })

	doc := &models.LocalModelManifest{
		APIVersion: "edgelet.iofog.org/v1",
		Kind:       "Model",
		Spec: models.LocalModelSpec{
			Repo:     "org/repo",
			Registry: 1,
		},
	}
	doc.Metadata.Name = "llama-2-7b-q2k"
	_, err := m.ApplyManifest(doc)
	if !errors.Is(err, ErrLocalModelsDisabled) {
		t.Fatalf("expected local models disabled, got %v", err)
	}
}

func TestStartPull_RefusedForLocalSourceWhenWatchdogEnabled(t *testing.T) {
	m, _, _ := newTestManager(t)
	if _, err := m.UpsertDesired(sampleModel("local-pull", "org/local-pull", "latest", 1, nil)); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	cfg := config.GetInstance()
	orig := cfg.WatchdogEnabled
	cfg.WatchdogEnabled = true
	t.Cleanup(func() { cfg.WatchdogEnabled = orig })

	_, err := m.StartPull("local-pull")
	if !errors.Is(err, ErrLocalModelsDisabled) {
		t.Fatalf("expected local pull refused, got %v", err)
	}
}

func TestPruneDangling_SkipsActivePull(t *testing.T) {
	m, ociPuller, _ := newTestManager(t)
	if _, err := m.UpsertDesired(sampleModel("pulling-model", "ai/pulling", "latest", 1, nil)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	ociPuller.block = make(chan struct{})
	op, err := m.StartPull("pulling-model")
	if err != nil {
		t.Fatalf("start pull: %v", err)
	}

	report, err := m.PruneDangling()
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	for _, name := range report.Removed {
		if name == "pulling-model" {
			t.Fatalf("active pull must not be deleted, got %+v", report)
		}
	}
	if _, err := m.db.GetLocalModel("pulling-model"); err != nil {
		t.Fatalf("pulling row must remain: %v", err)
	}
	if op == nil || op.Name != "pulling-model" {
		t.Fatalf("expected running pull, got %+v", op)
	}
	close(ociPuller.block)
	waitOp(t, m, op.OperationID)
}
