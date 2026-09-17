package models

import (
	"strings"
	"testing"
)

func validLocalRuntimeClassManifestForTest(t *testing.T) *LocalRuntimeClassManifest {
	t.Helper()
	doc := &LocalRuntimeClassManifest{
		APIVersion: "edgelet.iofog.org/v1",
		Kind:       "RuntimeClass",
		Handler:    "spin",
	}
	doc.Metadata.Name = "spin"
	return doc
}

func TestLocalRuntimeClassManifestValidateSuccess(t *testing.T) {
	doc := validLocalRuntimeClassManifestForTest(t)
	if err := doc.Validate(); err != nil {
		t.Fatalf("expected manifest to validate, got: %v", err)
	}
}

func TestLocalRuntimeClassManifestValidateReservedNameRejected(t *testing.T) {
	doc := validLocalRuntimeClassManifestForTest(t)
	doc.Metadata.Name = "crun"
	err := doc.Validate()
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("expected reserved-name validation error, got: %v", err)
	}
}

func TestLocalRuntimeClassManifestValidateLocalSuffixRejected(t *testing.T) {
	doc := validLocalRuntimeClassManifestForTest(t)
	doc.Metadata.Name = "custom-local"
	err := doc.Validate()
	if err == nil || !strings.Contains(err.Error(), "must not end with -local") {
		t.Fatalf("expected local-suffix validation error, got: %v", err)
	}
}

func TestLocalRuntimeClassManifestValidateMissingHandler(t *testing.T) {
	doc := validLocalRuntimeClassManifestForTest(t)
	doc.Handler = ""
	err := doc.Validate()
	if err == nil || !strings.Contains(err.Error(), "handler is required") {
		t.Fatalf("expected missing-handler validation error, got: %v", err)
	}
}

func TestIsReservedRuntimeClassName(t *testing.T) {
	if !IsReservedRuntimeClassName("crun") || !IsReservedRuntimeClassName("CRUN") {
		t.Fatal("expected crun to be reserved")
	}
	if IsReservedRuntimeClassName("spin") {
		t.Fatal("expected spin not to be reserved")
	}
}
