package models

import (
	"strings"
	"testing"
)

func validLocalModelManifest() *LocalModelManifest {
	doc := &LocalModelManifest{}
	doc.APIVersion = "edgelet.iofog.org/v1"
	doc.Kind = "Model"
	doc.Metadata.Name = "llama-2-7b-q2k"
	doc.Spec.Repo = "second-state/Llama-2-7B-Chat-GGUF"
	doc.Spec.Revision = "064fe43ea8c1e1f93477ef4a170bdc2b244ef02c"
	doc.Spec.Registry = 5
	doc.Spec.Files = []string{"llama-2-7b-chat.Q5_K_M.gguf"}
	doc.Spec.Format = ModelFormatGGUF
	return doc
}

func TestLocalModelManifestValidate_OK(t *testing.T) {
	doc := validLocalModelManifest()
	if err := doc.Validate(); err != nil {
		t.Fatalf("expected valid model to pass, got: %v", err)
	}
}

func TestLocalModelManifestValidate_NameDNS1123(t *testing.T) {
	invalidNames := []string{
		"Llama/2",
		"Llama_2",
		"llama/2",
		"-llama",
		"llama-",
		"llama.model",
		"UPPER",
		"",
	}
	for _, name := range invalidNames {
		doc := validLocalModelManifest()
		doc.Metadata.Name = name
		if err := doc.Validate(); err == nil {
			t.Fatalf("expected invalid name %q to fail validation", name)
		}
	}

	doc := validLocalModelManifest()
	doc.Metadata.Name = "llama-2-7b-q2k"
	if err := doc.Validate(); err != nil {
		t.Fatalf("expected dns-1123 name to pass, got: %v", err)
	}
}

func TestLocalModelManifestValidate_RepoRejectsSchemeAndHost(t *testing.T) {
	invalidRepos := []string{
		"",
		"https://huggingface.co/org/repo",
		"http://example.com/org/repo",
		"huggingface.co/org/repo",
		"docker.io/ai/gemma3",
		"localhost/org/repo",
		"/org/repo",
		"host:443/org/repo",
	}
	for _, repo := range invalidRepos {
		doc := validLocalModelManifest()
		doc.Spec.Repo = repo
		err := doc.Validate()
		if err == nil {
			t.Fatalf("expected repo %q to fail validation", repo)
		}
		if repo != "" && !strings.Contains(err.Error(), "spec.repo") {
			t.Fatalf("expected spec.repo error for %q, got: %v", repo, err)
		}
	}

	doc := validLocalModelManifest()
	doc.Spec.Repo = "ai/gemma3"
	if err := doc.Validate(); err != nil {
		t.Fatalf("expected path-only repo to pass, got: %v", err)
	}
}

func TestLocalModelManifestValidate_RegistryRequired(t *testing.T) {
	doc := validLocalModelManifest()
	doc.Spec.Registry = 0
	err := doc.Validate()
	if err == nil || !strings.Contains(err.Error(), "spec.registry") {
		t.Fatalf("expected spec.registry required, got: %v", err)
	}
}

func TestLocalModelManifestValidate_WithRegistryCompatibility(t *testing.T) {
	doc := validLocalModelManifest()
	hf := NewRegistryBuilder().SetID(5).SetURL("https://huggingface.co").SetType(RegistryTypeHF).Build()
	if err := doc.ValidateWithRegistry(hf); err != nil {
		t.Fatalf("expected matching hf registry to pass, got: %v", err)
	}

	wrongID := NewRegistryBuilder().SetID(1).SetURL("docker.io").SetType(RegistryTypeOCI).Build()
	err := doc.ValidateWithRegistry(wrongID)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected registry id mismatch, got: %v", err)
	}

	oci := NewRegistryBuilder().SetID(5).SetURL("quay.io").SetType(RegistryTypeOCI).Build()
	// files are ignored for OCI — not an error
	if err := doc.ValidateWithRegistry(oci); err != nil {
		t.Fatalf("expected oci registry with files to pass (files ignored), got: %v", err)
	}
}

func TestLocalModelManifest_ToLocalModel(t *testing.T) {
	doc := validLocalModelManifest()
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	row := doc.ToLocalModel()
	if row.Name != "llama-2-7b-q2k" || row.RegistryID != 5 || row.State != ModelStatePending {
		t.Fatalf("unexpected local model row: %+v", row)
	}
	if row.Source != ModelSourceLocal {
		t.Fatalf("expected local source, got %q", row.Source)
	}
	files := row.Files()
	if len(files) != 1 || files[0] != "llama-2-7b-chat.Q5_K_M.gguf" {
		t.Fatalf("unexpected files: %v", files)
	}
}
