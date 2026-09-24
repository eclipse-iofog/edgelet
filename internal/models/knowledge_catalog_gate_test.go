package models

import (
	"errors"
	"strings"
	"testing"
)

func knowledgeStatusLookup(m map[string]KnowledgeStatusInfo) KnowledgeStatusLookup {
	return func(name string) (KnowledgeStatusInfo, error) {
		info, ok := m[name]
		if !ok {
			return KnowledgeStatusInfo{}, nil
		}
		info.Exists = true
		return info, nil
	}
}

func TestEvaluateKnowledgeCatalogStartGate_Ready(t *testing.T) {
	c := &KnowledgeCatalog{
		BindPath: "/knowledge",
		Items:    []KnowledgeCatalogItem{{Name: "product-docs"}},
	}
	dec, msg, err := EvaluateKnowledgeCatalogStartGate(c, KnowledgeSourceLocal, knowledgeStatusLookup(map[string]KnowledgeStatusInfo{
		"product-docs": {Source: KnowledgeSourceLocal, State: KnowledgeStateReady},
	}))
	if err != nil || dec != CatalogGateAllow || msg != "" {
		t.Fatalf("expected allow, got decision=%v msg=%q err=%v", dec, msg, err)
	}
}

func TestEvaluateKnowledgeCatalogStartGate_Pulling(t *testing.T) {
	c := &KnowledgeCatalog{
		BindPath: "/knowledge",
		Items:    []KnowledgeCatalogItem{{Name: "product-docs"}},
	}
	dec, msg, err := EvaluateKnowledgeCatalogStartGate(c, KnowledgeSourceLocal, knowledgeStatusLookup(map[string]KnowledgeStatusInfo{
		"product-docs": {Source: KnowledgeSourceLocal, State: KnowledgeStatePulling},
	}))
	if err != nil || dec != CatalogGateWait {
		t.Fatalf("expected wait, got decision=%v err=%v", dec, err)
	}
	if !strings.Contains(msg, "product-docs") || !strings.Contains(msg, KnowledgeStatePulling) {
		t.Fatalf("expected wait text with knowledge name and state, got %q", msg)
	}
	if !strings.HasPrefix(msg, "waiting for knowledge download:") {
		t.Fatalf("expected knowledge wait prefix, got %q", msg)
	}
}

func TestEvaluateKnowledgeCatalogStartGate_Failed(t *testing.T) {
	c := &KnowledgeCatalog{
		BindPath: "/knowledge",
		Items:    []KnowledgeCatalogItem{{Name: "product-docs"}},
	}
	dec, msg, err := EvaluateKnowledgeCatalogStartGate(c, KnowledgeSourceManaged, knowledgeStatusLookup(map[string]KnowledgeStatusInfo{
		"product-docs": {Source: KnowledgeSourceManaged, State: KnowledgeStateFailed, LastError: "pull denied"},
	}))
	if err != nil || dec != CatalogGateFail {
		t.Fatalf("expected fail, got decision=%v err=%v", dec, err)
	}
	if !strings.Contains(msg, "Failed") || !strings.Contains(msg, "pull denied") {
		t.Fatalf("expected Failed knowledge text, got %q", msg)
	}
}

func TestEvaluateKnowledgeCatalogStartGate_ControllerNamesLocalOnly(t *testing.T) {
	c := &KnowledgeCatalog{
		BindPath: "/knowledge",
		Items:    []KnowledgeCatalogItem{{Name: "operator-docs"}},
	}
	dec, msg, err := EvaluateKnowledgeCatalogStartGate(c, KnowledgeSourceManaged, knowledgeStatusLookup(map[string]KnowledgeStatusInfo{
		"operator-docs": {Source: KnowledgeSourceLocal, State: KnowledgeStateReady},
	}))
	if err != nil || dec != CatalogGateFail {
		t.Fatalf("expected fail, got decision=%v err=%v", dec, err)
	}
	if !strings.Contains(msg, "local") {
		t.Fatalf("expected source-scope text, got %q", msg)
	}
}

func TestValidateKnowledgeCatalogApply_ControllerNamesLocalOnly(t *testing.T) {
	c := &KnowledgeCatalog{
		BindPath: "/knowledge",
		Items:    []KnowledgeCatalogItem{{Name: "operator-docs"}},
	}
	err := ValidateKnowledgeCatalogApply(c, KnowledgeSourceManaged, knowledgeStatusLookup(map[string]KnowledgeStatusInfo{
		"operator-docs": {Source: KnowledgeSourceLocal, State: KnowledgeStateReady},
	}))
	var scope *ErrKnowledgeSourceScope
	if !errors.As(err, &scope) || scope.Name != "operator-docs" || scope.Want != KnowledgeSourceManaged {
		t.Fatalf("expected controller source-scope apply error, got %v", err)
	}
}

func TestEvaluateKnowledgeCatalogStartGate_WrongSource(t *testing.T) {
	c := &KnowledgeCatalog{
		BindPath: "/knowledge",
		Items:    []KnowledgeCatalogItem{{Name: "fleet-docs"}},
	}
	dec, msg, err := EvaluateKnowledgeCatalogStartGate(c, KnowledgeSourceLocal, knowledgeStatusLookup(map[string]KnowledgeStatusInfo{
		"fleet-docs": {Source: KnowledgeSourceManaged, State: KnowledgeStateReady},
	}))
	if err != nil || dec != CatalogGateFail {
		t.Fatalf("expected fail, got decision=%v err=%v", dec, err)
	}
	if !strings.Contains(msg, "managed") {
		t.Fatalf("expected source-scope text, got %q", msg)
	}
}

func TestValidateKnowledgeCatalogApply_UnknownName(t *testing.T) {
	c := &KnowledgeCatalog{
		BindPath: "/knowledge",
		Items:    []KnowledgeCatalogItem{{Name: "missing-docs"}},
	}
	err := ValidateKnowledgeCatalogApply(c, KnowledgeSourceLocal, knowledgeStatusLookup(nil))
	var scope *ErrKnowledgeSourceScope
	if !errors.As(err, &scope) || !scope.Missing || scope.Name != "missing-docs" {
		t.Fatalf("expected missing-name apply error, got %v", err)
	}
}

func TestValidateKnowledgeCatalogApply_Failed(t *testing.T) {
	c := &KnowledgeCatalog{
		BindPath: "/knowledge",
		Items:    []KnowledgeCatalogItem{{Name: "product-docs"}},
	}
	err := ValidateKnowledgeCatalogApply(c, KnowledgeSourceLocal, knowledgeStatusLookup(map[string]KnowledgeStatusInfo{
		"product-docs": {Source: KnowledgeSourceLocal, State: KnowledgeStateFailed, LastError: "checksum mismatch"},
	}))
	var failed *ErrKnowledgeFailed
	if !errors.As(err, &failed) || failed.Name != "product-docs" {
		t.Fatalf("expected Failed apply error, got %v", err)
	}
}

func TestValidateKnowledgeCatalogApply_PendingAllowed(t *testing.T) {
	c := &KnowledgeCatalog{
		BindPath: "/knowledge",
		Items:    []KnowledgeCatalogItem{{Name: "product-docs"}},
	}
	err := ValidateKnowledgeCatalogApply(c, KnowledgeSourceLocal, knowledgeStatusLookup(map[string]KnowledgeStatusInfo{
		"product-docs": {Source: KnowledgeSourceLocal, State: KnowledgeStatePending},
	}))
	if err != nil {
		t.Fatalf("pending should be allowed at apply: %v", err)
	}
}

func TestKnowledgeCatalogMountNeedsRecreate(t *testing.T) {
	base := &KnowledgeCatalog{
		BindPath:    "/knowledge",
		Permissions: KnowledgeCatalogPermRO,
		Items:       []KnowledgeCatalogItem{{Name: "a"}},
	}
	added := base.Clone()
	added.Items = append(added.Items, KnowledgeCatalogItem{Name: "b"})
	if KnowledgeCatalogMountNeedsRecreate(base, added) {
		t.Fatal("adding an item must not recreate")
	}

	bind := base.Clone()
	bind.BindPath = "/corpus"
	if !KnowledgeCatalogMountNeedsRecreate(base, bind) {
		t.Fatal("bindPath change must recreate")
	}

	perms := base.Clone()
	perms.Permissions = KnowledgeCatalogPermRW
	if !KnowledgeCatalogMountNeedsRecreate(base, perms) {
		t.Fatal("permissions change must recreate")
	}

	empty := &KnowledgeCatalog{BindPath: "/knowledge", Permissions: KnowledgeCatalogPermRO}
	if !KnowledgeCatalogMountNeedsRecreate(nil, base) {
		t.Fatal("empty to nonempty catalog must recreate")
	}
	if !KnowledgeCatalogMountNeedsRecreate(base, empty) {
		t.Fatal("removing the last catalog item must recreate")
	}
	if KnowledgeCatalogMountNeedsRecreate(nil, empty) {
		t.Fatal("empty to empty must not recreate")
	}
}

func TestCombineCatalogStartGates(t *testing.T) {
	dec, msg := CombineCatalogStartGates(CatalogGateAllow, "", CatalogGateWait, "waiting for knowledge download: product-docs (Pulling)")
	if dec != CatalogGateWait || !strings.Contains(msg, "product-docs") {
		t.Fatalf("models Ready + knowledge Pulling must wait, got dec=%v msg=%q", dec, msg)
	}

	dec, msg = CombineCatalogStartGates(CatalogGateWait, "waiting for model download: llama (Pulling)", CatalogGateAllow, "")
	if dec != CatalogGateWait || !strings.Contains(msg, "llama") {
		t.Fatalf("knowledge Ready + model Pulling must wait, got dec=%v msg=%q", dec, msg)
	}

	dec, msg = CombineCatalogStartGates(CatalogGateAllow, "", CatalogGateAllow, "")
	if dec != CatalogGateAllow || msg != "" {
		t.Fatalf("both Ready must allow, got dec=%v msg=%q", dec, msg)
	}

	dec, msg = CombineCatalogStartGates(CatalogGateFail, "catalog item \"llama\" is Failed", CatalogGateWait, "waiting")
	if dec != CatalogGateFail || !strings.Contains(msg, "Failed") {
		t.Fatalf("model fail must win, got dec=%v msg=%q", dec, msg)
	}
}
