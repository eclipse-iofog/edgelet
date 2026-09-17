package runtimeapi

import (
	"errors"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/modelmanager"
	"github.com/eclipse-iofog/edgelet/internal/models"
)

func TestFacadeApplyLocalModelManifests_WritesRow(t *testing.T) {
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
kind: Model
metadata:
  name: llama-2-7b-q2k
spec:
  repo: second-state/Llama-2-7B-Chat-GGUF
  revision: 064fe43ea8c1e1f93477ef4a170bdc2b244ef02c
  registry: 5
  files:
    - llama-2-7b-chat.Q5_K_M.gguf
  format: gguf
`) + "\n"
	rows, err := f.ApplyLocalModelManifests(manifest, false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "llama-2-7b-q2k" {
		t.Fatalf("unexpected apply rows: %+v", rows)
	}
	got, err := f.GetModel("llama-2-7b-q2k")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got["state"] != models.ModelStatePending {
		t.Fatalf("expected Pending after apply, got %#v", got)
	}
}

func TestFacadeApplyExampleModelSet(t *testing.T) {
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
kind: Model
metadata:
  name: llama-2-7b-q2k
spec:
  repo: second-state/Llama-2-7B-Chat-GGUF
  revision: 064fe43ea8c1e1f93477ef4a170bdc2b244ef02c
  registry: 5
  files:
    - llama-2-7b-chat.Q5_K_M.gguf
  format: gguf
---
apiVersion: edgelet.iofog.org/v1
kind: Model
metadata:
  name: gemma3
  labels:
    family: gemma3
spec:
  repo: ai/gemma3
  revision: 4b-q8_0
  registry: 1
  files: []
  format: gguf
---
apiVersion: edgelet.iofog.org/v1
kind: Model
metadata:
  name: gemma3-4b-q4-k-m
  labels:
    family: gemma3
spec:
  repo: ai/gemma3
  revision: sha256:1eca257ec64d465cf38f766561e28eddb3b764adfba703288106a31cd2fe84a8
  registry: 1
  files: []
  format: gguf
---
apiVersion: edgelet.iofog.org/v1
kind: Model
metadata:
  name: qwen3-8-27b
  labels:
    family: qwen
spec:
  repo: Qwen/Qwen3.8-27B
  revision: main
  registry: 5
  files:
    - model-00001-of-00018.safetensors
    - tokenizer.json
    - config.json
  format: safetensors
`) + "\n"

	docs, err := f.ParseAndValidateLocalModelManifests(manifest)
	if err != nil {
		t.Fatalf("parse/validate: %v", err)
	}
	if len(docs) != 4 {
		t.Fatalf("expected 4 model documents, got %d", len(docs))
	}
	rows, err := f.ApplyLocalModelManifests(manifest, false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("expected 4 applied rows, got %d", len(rows))
	}
	want := map[string]bool{
		"llama-2-7b-q2k": true, "gemma3": true, "gemma3-4b-q4-k-m": true, "qwen3-8-27b": true,
	}
	for _, row := range rows {
		if !want[row.Name] {
			t.Fatalf("unexpected model name %q", row.Name)
		}
		if row.State != models.ModelStatePending {
			t.Fatalf("expected Pending after apply for %s, got %s", row.Name, row.State)
		}
		delete(want, row.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing applied models: %v", want)
	}
}

func TestFacadeListGetModel_SourceAndInspectExtras(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })

	local := &models.LocalModel{
		Name:       "operator-model",
		Source:     models.ModelSourceLocal,
		Repo:       "org/local",
		RegistryID: 1,
		State:      models.ModelStateReady,
	}
	if err := f.db.UpsertLocalModel(local); err != nil {
		t.Fatalf("upsert local: %v", err)
	}
	managed := &models.LocalModel{
		Name:       "fleet-model",
		Source:     models.ModelSourceManaged,
		Repo:       "org/fleet",
		RegistryID: 5,
		State:      models.ModelStateReady,
	}
	if err := f.db.UpsertLocalModel(managed); err != nil {
		t.Fatalf("upsert managed: %v", err)
	}
	if err := f.db.SaveControllerModels([]*models.ControllerModel{{
		UUID:       "3f2c8a1e-2b64-4c0d-9f11-0a1b2c3d4e5f",
		Name:       "fleet-model",
		Repo:       "org/fleet",
		RegistryID: 5,
	}}); err != nil {
		t.Fatalf("save controller model: %v", err)
	}
	if err := f.db.ReplaceWorkloadModelRefs("ms-1", []string{"fleet-model"}); err != nil {
		t.Fatalf("bind ref: %v", err)
	}

	items, err := f.ListModels()
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
	if sources["operator-model"] != models.ModelSourceLocal || sources["fleet-model"] != models.ModelSourceManaged {
		t.Fatalf("expected local and managed sources, got %#v", sources)
	}

	got, err := f.GetModel("fleet-model")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if got["source"] != models.ModelSourceManaged {
		t.Fatalf("expected managed source, got %#v", got)
	}
	if got["uuid"] != "3f2c8a1e-2b64-4c0d-9f11-0a1b2c3d4e5f" {
		t.Fatalf("expected managed uuid, got %#v", got)
	}
	if got["bindRefCount"] != 1 {
		t.Fatalf("expected bindRefCount 1, got %#v", got)
	}

	localInspect, err := f.GetModel("operator-model")
	if err != nil {
		t.Fatalf("local inspect: %v", err)
	}
	if localInspect["source"] != models.ModelSourceLocal {
		t.Fatalf("expected local source, got %#v", localInspect)
	}
	if _, ok := localInspect["uuid"]; ok {
		t.Fatalf("local inspect must omit uuid, got %#v", localInspect)
	}
}

func TestFacadeParseAndValidateLocalModelManifests_RejectsHostRepo(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })
	_, err := f.ParseAndValidateLocalModelManifests(`
apiVersion: edgelet.iofog.org/v1
kind: Model
metadata:
  name: llama-2-7b-q2k
spec:
  repo: huggingface.co/org/repo
  registry: 5
`)
	if err == nil || !strings.Contains(err.Error(), "spec.repo") {
		t.Fatalf("expected spec.repo validation error, got: %v", err)
	}
}

func TestFacadeModelManager_AppliesLiveDiskThresholdAfterConfigChange(t *testing.T) {
	f := NewFacade()
	dir := t.TempDir()
	originalThreshold := f.cfg.AvailableDiskThreshold
	originalDir := f.cfg.DiskDirectory
	t.Cleanup(func() {
		f.cfg.AvailableDiskThreshold = originalThreshold
		f.cfg.DiskDirectory = originalDir
	})
	f.cfg.DiskDirectory = dir
	f.cfg.AvailableDiskThreshold = 20

	mm := f.modelManager()
	mm.SetDiskPolicy(dir, 20, func(string) (int64, int64, error) {
		return 100, 1000, nil
	})
	if err := mm.Ensure(50); err == nil || !strings.Contains(err.Error(), "available-disk threshold is 20%") {
		t.Fatalf("cached manager should reject at 20%%, got %v", err)
	}

	f.cfg.AvailableDiskThreshold = 1
	if err := mm.Ensure(50); err != nil {
		t.Fatalf("config change to 1%% must apply without recreating the manager, got %v", err)
	}
}

func TestFacadeApplyLocalModelManifests_RefusedWhenWatchdogEnabled(t *testing.T) {
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

	_, err := f.ApplyLocalModelManifests(`
apiVersion: edgelet.iofog.org/v1
kind: Model
metadata:
  name: llama-2-7b-q2k
spec:
  repo: org/repo
  registry: 1
`, true)
	if !errors.Is(err, modelmanager.ErrLocalModelsDisabled) {
		t.Fatalf("expected local Model apply refused while watchdog is on, got %v", err)
	}
}
