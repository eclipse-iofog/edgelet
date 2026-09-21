package models

import (
	"strings"
	"testing"
)

func TestValidate_KnowledgeItemsWithoutBindPath(t *testing.T) {
	doc := validLocalDeployManifestForTest("kb-nobind")
	doc.Spec.Knowledge = &KnowledgeCatalog{Items: []KnowledgeCatalogItem{{Name: "product-docs"}}}
	err := doc.Validate()
	if err == nil || !strings.Contains(err.Error(), "bindPath is required") {
		t.Fatalf("expected bindPath required, got %v", err)
	}
}

func TestValidate_KnowledgePermissionsDefaultRO(t *testing.T) {
	doc := validLocalDeployManifestForTest("kb-perm-default")
	doc.Spec.Knowledge = &KnowledgeCatalog{
		BindPath: "/knowledge",
		Items:    []KnowledgeCatalogItem{{Name: "product-docs"}},
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if doc.Spec.Knowledge.Permissions != KnowledgeCatalogPermRO {
		t.Fatalf("expected default ro, got %q", doc.Spec.Knowledge.Permissions)
	}
}

func TestValidate_KnowledgeDuplicateItemName(t *testing.T) {
	doc := validLocalDeployManifestForTest("kb-dup")
	doc.Spec.Knowledge = &KnowledgeCatalog{
		BindPath: "/knowledge",
		Items: []KnowledgeCatalogItem{
			{Name: "product-docs"},
			{Name: "product-docs"},
		},
	}
	err := doc.Validate()
	if err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("expected duplicate name error, got %v", err)
	}
}

func TestValidate_KnowledgeBindPathCollidesWithModels(t *testing.T) {
	doc := validLocalDeployManifestForTest("kb-models")
	doc.Spec.Models = &ModelCatalog{
		BindPath: "/shared",
		Items:    []ModelCatalogItem{{Name: "llama-2-7b-q2k"}},
	}
	doc.Spec.Knowledge = &KnowledgeCatalog{
		BindPath: "/shared",
		Items:    []KnowledgeCatalogItem{{Name: "product-docs"}},
	}
	err := doc.Validate()
	if err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("expected models catalog collision, got %v", err)
	}
}

func TestValidate_KnowledgeBindPathCollidesWithVolume(t *testing.T) {
	doc := validLocalDeployManifestForTest("kb-vol")
	doc.Spec.Container.Volumes = []struct {
		HostDestination      string `yaml:"hostDestination" json:"hostDestination"`
		ContainerDestination string `yaml:"containerDestination" json:"containerDestination"`
		AccessMode           string `yaml:"accessMode,omitempty" json:"accessMode,omitempty"`
		Type                 string `yaml:"type,omitempty" json:"type,omitempty"`
		Scope                string `yaml:"scope,omitempty" json:"scope,omitempty"`
	}{{
		HostDestination:      "/var/lib/data",
		ContainerDestination: "/knowledge",
		Type:                 "BIND",
	}}
	doc.Spec.Knowledge = &KnowledgeCatalog{
		BindPath: "/knowledge",
		Items:    []KnowledgeCatalogItem{{Name: "product-docs"}},
	}
	err := doc.Validate()
	if err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("expected volume collision, got %v", err)
	}
}

func TestValidate_KnowledgeAndModelsDistinctBindPathsOK(t *testing.T) {
	doc := validLocalDeployManifestForTest("kb-both")
	doc.Spec.Models = &ModelCatalog{
		BindPath: "/models",
		Items:    []ModelCatalogItem{{Name: "llama-2-7b-q2k"}},
	}
	doc.Spec.Knowledge = &KnowledgeCatalog{
		BindPath: "/knowledge",
		Items:    []KnowledgeCatalogItem{{Name: "product-docs"}},
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("distinct catalog bind paths should pass: %v", err)
	}
}

func TestValidate_KnowledgeItemPathCollidesWithModelItem(t *testing.T) {
	doc := validLocalDeployManifestForTest("kb-item-path")
	doc.Spec.Models = &ModelCatalog{
		BindPath: "/data",
		Items:    []ModelCatalogItem{{Name: "shared"}},
	}
	doc.Spec.Knowledge = &KnowledgeCatalog{
		BindPath: "/data",
		Items:    []KnowledgeCatalogItem{{Name: "other"}},
	}
	err := doc.Validate()
	if err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("expected models bindPath collision, got %v", err)
	}
}

func TestValidate_KnowledgeEmptyItemsNoBindPathRequired(t *testing.T) {
	doc := validLocalDeployManifestForTest("kb-empty")
	if err := doc.Validate(); err != nil {
		t.Fatalf("omitted knowledge should pass: %v", err)
	}
	doc.Spec.Knowledge = &KnowledgeCatalog{Items: nil}
	if err := doc.Validate(); err != nil {
		t.Fatalf("empty items should pass without bindPath: %v", err)
	}
}

func TestValidate_ControllerKnowledgeCatalog(t *testing.T) {
	ms := NewMicroservice("ms-kb", "nginx:latest")
	ms.Knowledge = &KnowledgeCatalog{Items: []KnowledgeCatalogItem{{Name: "product-docs"}}}
	err := ms.Validate()
	if err == nil || !strings.Contains(err.Error(), "bindPath is required") {
		t.Fatalf("expected bindPath required on controller MS, got %v", err)
	}

	ms.Knowledge = &KnowledgeCatalog{
		BindPath: "/knowledge",
		Items:    []KnowledgeCatalogItem{{Name: "product-docs"}},
	}
	if err := ms.Validate(); err != nil {
		t.Fatalf("valid knowledge catalog should pass: %v", err)
	}
}
