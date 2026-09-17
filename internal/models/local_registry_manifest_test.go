package models

import (
	"encoding/base64"
	"strings"
	"testing"
)

func validLocalRegistryManifest() *LocalRegistryManifest {
	return &LocalRegistryManifest{
		APIVersion: "edgelet.iofog.org/v1",
		Kind:       "Registry",
		Spec: LocalRegistrySpec{
			URL:     "registry.example.com",
			Private: false,
		},
	}
}

func TestLocalRegistryManifestValidate_DefaultTypeOCI(t *testing.T) {
	doc := validLocalRegistryManifest()
	if err := doc.Validate(); err != nil {
		t.Fatalf("expected omitted type to validate, got: %v", err)
	}
	if doc.Spec.Type != RegistryTypeOCI {
		t.Fatalf("expected default type oci, got %q", doc.Spec.Type)
	}
}

func TestLocalRegistryManifestValidate_PrivateRequiresUsernameAndPassword(t *testing.T) {
	doc := validLocalRegistryManifest()
	doc.Spec.Private = true
	doc.Spec.Password = "secret"

	err := doc.Validate()
	if err == nil || err.Error() != "spec.username is required when spec.private=true" {
		t.Fatalf("expected missing username validation error, got: %v", err)
	}

	doc.Spec.UserName = "alice"
	doc.Spec.Password = ""
	err = doc.Validate()
	if err == nil || err.Error() != "spec.password is required when spec.private=true" {
		t.Fatalf("expected missing password validation error, got: %v", err)
	}
}

func TestLocalRegistryManifestValidate_PrivateWithCredentialsAllowsEmptyEmail(t *testing.T) {
	doc := validLocalRegistryManifest()
	doc.Spec.Private = true
	doc.Spec.UserName = "alice"
	doc.Spec.Password = "secret"
	doc.Spec.UserEmail = ""

	if err := doc.Validate(); err != nil {
		t.Fatalf("expected private registry with username/password and empty email to validate, got: %v", err)
	}
}

func TestLocalRegistryManifestValidate_HFPrivatePasswordOnly(t *testing.T) {
	doc := validLocalRegistryManifest()
	doc.Spec.Type = RegistryTypeHF
	doc.Spec.URL = "https://huggingface.co"
	doc.Spec.Private = true
	doc.Spec.Password = "hf_token"

	if err := doc.Validate(); err != nil {
		t.Fatalf("expected hf private with token-only to validate, got: %v", err)
	}

	doc.Spec.Password = ""
	err := doc.Validate()
	if err == nil || err.Error() != "spec.password is required when spec.private=true" {
		t.Fatalf("expected hf private to require password, got: %v", err)
	}
}

func TestLocalRegistryManifestValidate_HFRejectsEmail(t *testing.T) {
	doc := validLocalRegistryManifest()
	doc.Spec.Type = RegistryTypeHF
	doc.Spec.UserEmail = "user@example.com"
	err := doc.Validate()
	if err == nil || !strings.Contains(err.Error(), "spec.email") {
		t.Fatalf("expected hf email rejection, got: %v", err)
	}
}

func TestLocalRegistryManifestValidate_RejectsUnknownType(t *testing.T) {
	doc := validLocalRegistryManifest()
	doc.Spec.Type = "git"
	err := doc.Validate()
	if err == nil || !strings.Contains(err.Error(), "spec.type") {
		t.Fatalf("expected unknown type error, got: %v", err)
	}
}

func TestLocalRegistryManifestValidate_CAMustBeBase64(t *testing.T) {
	doc := validLocalRegistryManifest()
	doc.Spec.CA = "not-base64!!!"
	err := doc.Validate()
	if err == nil || !strings.Contains(err.Error(), "spec.ca") {
		t.Fatalf("expected invalid ca error, got: %v", err)
	}

	doc.Spec.CA = base64.StdEncoding.EncodeToString([]byte("-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"))
	if err := doc.Validate(); err != nil {
		t.Fatalf("expected valid base64 ca to pass, got: %v", err)
	}
}

func TestLocalRegistryManifest_CollisionWithDifferentURL(t *testing.T) {
	doc := validLocalRegistryManifest()
	doc.Spec.ID = 5
	doc.Spec.Type = RegistryTypeOCI
	doc.Spec.URL = "quay.io"
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	existing := NewRegistry(5, "ghcr.io", true, "", "", "")
	err := doc.CollisionWith(existing)
	if err == nil || !strings.Contains(err.Error(), "collision") {
		t.Fatalf("expected id collision on different url, got: %v", err)
	}

	same := NewRegistry(5, "quay.io", false, "alice", "secret", "")
	if err := doc.CollisionWith(same); err != nil {
		t.Fatalf("expected same type+url upsert to pass, got: %v", err)
	}
}

func TestRegistryIdentityCollision_DifferentType(t *testing.T) {
	existing := NewRegistryBuilder().SetID(5).SetURL("https://huggingface.co").SetType(RegistryTypeHF).Build()
	incoming := NewRegistryBuilder().SetID(5).SetURL("https://huggingface.co").SetType(RegistryTypeOCI).Build()
	err := RegistryIdentityCollision(existing, incoming)
	if err == nil || !strings.Contains(err.Error(), "collision") {
		t.Fatalf("expected type collision, got: %v", err)
	}
}

func TestRequireOCIForImagePull(t *testing.T) {
	if err := RequireOCIForImagePull(nil); err != nil {
		t.Fatalf("nil registry should be allowed: %v", err)
	}
	oci := NewRegistry(1, "docker.io", true, "", "", "")
	if err := RequireOCIForImagePull(oci); err != nil {
		t.Fatalf("oci registry should be allowed: %v", err)
	}
	hf := NewRegistryBuilder().SetID(5).SetURL("https://huggingface.co").SetType(RegistryTypeHF).Build()
	err := RequireOCIForImagePull(hf)
	if err == nil || !strings.Contains(err.Error(), "oci") {
		t.Fatalf("expected oci requirement error, got: %v", err)
	}
}

func TestRequireRegistryType_Mismatch(t *testing.T) {
	oci := NewRegistry(1, "quay.io", true, "", "", "")
	err := RequireRegistryType(oci, RegistryTypeHF)
	if err == nil || !strings.Contains(err.Error(), "hf") {
		t.Fatalf("expected type mismatch for hf pull against oci registry, got: %v", err)
	}
	if err := RequireRegistryType(oci, RegistryTypeOCI); err != nil {
		t.Fatalf("oci registry should match oci pull: %v", err)
	}
	if err := RequireRegistryType(nil, RegistryTypeOCI); err == nil {
		t.Fatal("expected error for nil registry")
	}
}
