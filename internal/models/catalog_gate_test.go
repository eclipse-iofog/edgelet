package models

import (
	"errors"
	"strings"
	"testing"
)

func statusLookup(m map[string]ModelStatusInfo) ModelStatusLookup {
	return func(name string) (ModelStatusInfo, error) {
		info, ok := m[name]
		if !ok {
			return ModelStatusInfo{}, nil
		}
		info.Exists = true
		return info, nil
	}
}

func TestEvaluateCatalogStartGate_Ready(t *testing.T) {
	c := &ModelCatalog{
		BindPath: "/models",
		Items:    []ModelCatalogItem{{Name: "test-model"}},
	}
	dec, msg, err := EvaluateCatalogStartGate(c, ModelSourceLocal, statusLookup(map[string]ModelStatusInfo{
		"test-model": {Source: ModelSourceLocal, State: ModelStateReady},
	}))
	if err != nil || dec != CatalogGateAllow || msg != "" {
		t.Fatalf("expected allow, got decision=%v msg=%q err=%v", dec, msg, err)
	}
}

func TestEvaluateCatalogStartGate_Pulling(t *testing.T) {
	c := &ModelCatalog{
		BindPath: "/models",
		Items:    []ModelCatalogItem{{Name: "test-model"}},
	}
	dec, msg, err := EvaluateCatalogStartGate(c, ModelSourceLocal, statusLookup(map[string]ModelStatusInfo{
		"test-model": {Source: ModelSourceLocal, State: ModelStatePulling},
	}))
	if err != nil || dec != CatalogGateWait {
		t.Fatalf("expected wait, got decision=%v err=%v", dec, err)
	}
	if !strings.Contains(msg, "test-model") || !strings.Contains(msg, ModelStatePulling) {
		t.Fatalf("expected wait text with model name and state, got %q", msg)
	}
}

func TestFormatCatalogWaitMessage(t *testing.T) {
	if got := formatCatalogWaitMessage(nil); got != CatalogWaitingMessage {
		t.Fatalf("empty wait list: %q", got)
	}
	got := formatCatalogWaitMessage([]catalogWaitEntry{
		{Name: "test-model", State: ModelStatePulling},
		{Name: "qwen3-8-27b", State: ModelStatePending},
	})
	if !strings.Contains(got, "test-model") || !strings.Contains(got, ModelStatePulling) {
		t.Fatalf("expected named wait text, got %q", got)
	}
	if !strings.Contains(got, "qwen3-8-27b") || !strings.Contains(got, ModelStatePending) {
		t.Fatalf("expected both waiting models, got %q", got)
	}
}

func TestEvaluateCatalogStartGate_Failed(t *testing.T) {
	c := &ModelCatalog{
		BindPath: "/models",
		Items:    []ModelCatalogItem{{Name: "test-model"}},
	}
	dec, msg, err := EvaluateCatalogStartGate(c, ModelSourceManaged, statusLookup(map[string]ModelStatusInfo{
		"test-model": {Source: ModelSourceManaged, State: ModelStateFailed, LastError: "pull denied"},
	}))
	if err != nil || dec != CatalogGateFail {
		t.Fatalf("expected fail, got decision=%v err=%v", dec, err)
	}
	if !strings.Contains(msg, "Failed") || !strings.Contains(msg, "pull denied") {
		t.Fatalf("expected Failed model text, got %q", msg)
	}
}

func TestEvaluateCatalogStartGate_WrongSource(t *testing.T) {
	c := &ModelCatalog{
		BindPath: "/models",
		Items:    []ModelCatalogItem{{Name: "fleet-model"}},
	}
	dec, msg, err := EvaluateCatalogStartGate(c, ModelSourceLocal, statusLookup(map[string]ModelStatusInfo{
		"fleet-model": {Source: ModelSourceManaged, State: ModelStateReady},
	}))
	if err != nil || dec != CatalogGateFail {
		t.Fatalf("expected fail, got decision=%v err=%v", dec, err)
	}
	if !strings.Contains(msg, "managed") {
		t.Fatalf("expected source-scope text, got %q", msg)
	}
}

func TestValidateCatalogApply_UnknownName(t *testing.T) {
	c := &ModelCatalog{
		BindPath: "/models",
		Items:    []ModelCatalogItem{{Name: "missing-model"}},
	}
	err := ValidateCatalogApply(c, ModelSourceLocal, statusLookup(nil))
	var scope *ErrModelSourceScope
	if !errors.As(err, &scope) || !scope.Missing || scope.Name != "missing-model" {
		t.Fatalf("expected missing-name apply error, got %v", err)
	}
}

func TestEvaluateCatalogStartGate_UnknownName(t *testing.T) {
	c := &ModelCatalog{
		BindPath: "/models",
		Items:    []ModelCatalogItem{{Name: "missing-model"}},
	}
	dec, msg, err := EvaluateCatalogStartGate(c, ModelSourceLocal, statusLookup(nil))
	if err != nil || dec != CatalogGateFail {
		t.Fatalf("expected fail, got decision=%v err=%v", dec, err)
	}
	if !strings.Contains(msg, "missing-model") {
		t.Fatalf("expected missing name in status text, got %q", msg)
	}
}

func TestValidateCatalogApply_Failed(t *testing.T) {
	c := &ModelCatalog{
		BindPath: "/models",
		Items:    []ModelCatalogItem{{Name: "test-model"}},
	}
	err := ValidateCatalogApply(c, ModelSourceLocal, statusLookup(map[string]ModelStatusInfo{
		"test-model": {Source: ModelSourceLocal, State: ModelStateFailed, LastError: "checksum mismatch"},
	}))
	var failed *ErrModelFailed
	if !errors.As(err, &failed) || failed.Name != "test-model" {
		t.Fatalf("expected Failed apply error, got %v", err)
	}
}

func TestValidateCatalogApply_PendingAllowed(t *testing.T) {
	c := &ModelCatalog{
		BindPath: "/models",
		Items:    []ModelCatalogItem{{Name: "test-model"}},
	}
	err := ValidateCatalogApply(c, ModelSourceLocal, statusLookup(map[string]ModelStatusInfo{
		"test-model": {Source: ModelSourceLocal, State: ModelStatePending},
	}))
	if err != nil {
		t.Fatalf("pending should be allowed at apply: %v", err)
	}
}

func TestCatalogMountNeedsRecreate(t *testing.T) {
	base := &ModelCatalog{
		BindPath:    "/models",
		Permissions: ModelCatalogPermRO,
		Items:       []ModelCatalogItem{{Name: "a"}},
	}
	added := base.Clone()
	added.Items = append(added.Items, ModelCatalogItem{Name: "b"})
	if CatalogMountNeedsRecreate(base, added) {
		t.Fatal("adding an item must not recreate")
	}

	bind := base.Clone()
	bind.BindPath = "/weights"
	if !CatalogMountNeedsRecreate(base, bind) {
		t.Fatal("bindPath change must recreate")
	}

	perms := base.Clone()
	perms.Permissions = ModelCatalogPermRW
	if !CatalogMountNeedsRecreate(base, perms) {
		t.Fatal("permissions change must recreate")
	}

	empty := &ModelCatalog{BindPath: "/models", Permissions: ModelCatalogPermRO}
	if !CatalogMountNeedsRecreate(nil, base) {
		t.Fatal("empty to nonempty catalog must recreate")
	}
	if !CatalogMountNeedsRecreate(base, empty) {
		t.Fatal("removing the last catalog item must recreate")
	}
	if CatalogMountNeedsRecreate(nil, empty) {
		t.Fatal("empty to empty must not recreate")
	}
}
