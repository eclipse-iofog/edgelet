package models

import (
	"strings"
	"testing"
)

func validLocalKnowledgeManifest() *LocalKnowledgeManifest {
	doc := &LocalKnowledgeManifest{}
	doc.APIVersion = "edgelet.iofog.org/v1"
	doc.Kind = "Knowledge"
	doc.Metadata.Name = "product-docs"
	doc.Spec.Repo = "acme/product-manuals"
	doc.Spec.Revision = "9f3c111122223333444455556666777788889999"
	doc.Spec.Registry = 3
	doc.Spec.Files = []string{"data/**/*.jsonl", "index/faiss.index"}
	doc.Spec.Format = KnowledgeFormatJSONL
	return doc
}

func TestLocalKnowledgeManifestValidate_OK(t *testing.T) {
	doc := validLocalKnowledgeManifest()
	if err := doc.Validate(); err != nil {
		t.Fatalf("expected valid knowledge to pass, got: %v", err)
	}
}

func TestLocalKnowledgeManifestValidate_NameDNS1123(t *testing.T) {
	invalidNames := []string{
		"Docs/1",
		"Docs_1",
		"docs/1",
		"-docs",
		"docs-",
		"docs.kb",
		"UPPER",
		"",
	}
	for _, name := range invalidNames {
		doc := validLocalKnowledgeManifest()
		doc.Metadata.Name = name
		if err := doc.Validate(); err == nil {
			t.Fatalf("expected invalid name %q to fail validation", name)
		}
	}

	doc := validLocalKnowledgeManifest()
	doc.Metadata.Name = "product-docs"
	if err := doc.Validate(); err != nil {
		t.Fatalf("expected dns-1123 name to pass, got: %v", err)
	}
}

func TestLocalKnowledgeManifestValidate_RepoRejectsSchemeAndHost(t *testing.T) {
	invalidRepos := []string{
		"",
		"https://huggingface.co/org/dataset",
		"http://example.com/org/dataset",
		"huggingface.co/org/dataset",
		"docker.io/acme/docs",
		"localhost/org/dataset",
		"/org/dataset",
		"host:443/org/dataset",
	}
	for _, repo := range invalidRepos {
		doc := validLocalKnowledgeManifest()
		doc.Spec.Repo = repo
		err := doc.Validate()
		if err == nil {
			t.Fatalf("expected repo %q to fail validation", repo)
		}
		if repo != "" && !strings.Contains(err.Error(), "spec.repo") {
			t.Fatalf("expected spec.repo error for %q, got: %v", repo, err)
		}
	}

	doc := validLocalKnowledgeManifest()
	doc.Spec.Repo = "acme/product-manuals"
	if err := doc.Validate(); err != nil {
		t.Fatalf("expected path-only repo to pass, got: %v", err)
	}
}

func TestLocalKnowledgeManifestValidate_FormatAllowlist(t *testing.T) {
	t.Run("pdf accepted", func(t *testing.T) {
		doc := validLocalKnowledgeManifest()
		doc.Spec.Format = KnowledgeFormatPDF
		if err := doc.Validate(); err != nil {
			t.Fatalf("expected pdf format to pass, got: %v", err)
		}
	})
	t.Run("omitted accepted", func(t *testing.T) {
		doc := validLocalKnowledgeManifest()
		doc.Spec.Format = ""
		if err := doc.Validate(); err != nil {
			t.Fatalf("expected omitted format to pass, got: %v", err)
		}
	})
	t.Run("unknown accepted", func(t *testing.T) {
		doc := validLocalKnowledgeManifest()
		doc.Spec.Format = KnowledgeFormatUnknown
		if err := doc.Validate(); err != nil {
			t.Fatalf("expected unknown format to pass, got: %v", err)
		}
	})
	t.Run("gguf rejected", func(t *testing.T) {
		doc := validLocalKnowledgeManifest()
		doc.Spec.Format = ModelFormatGGUF
		err := doc.Validate()
		if err == nil || !strings.Contains(err.Error(), "spec.format") {
			t.Fatalf("expected gguf format to fail, got: %v", err)
		}
	})
}

func TestLocalKnowledgeManifestValidate_RegistryRequired(t *testing.T) {
	doc := validLocalKnowledgeManifest()
	doc.Spec.Registry = 0
	err := doc.Validate()
	if err == nil || !strings.Contains(err.Error(), "spec.registry") {
		t.Fatalf("expected spec.registry required, got: %v", err)
	}
}

func TestLocalKnowledgeManifest_ToLocalKnowledge(t *testing.T) {
	doc := validLocalKnowledgeManifest()
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	row := doc.ToLocalKnowledge()
	if row.Name != "product-docs" || row.RegistryID != 3 || row.State != KnowledgeStatePending {
		t.Fatalf("unexpected local knowledge row: %+v", row)
	}
	if row.Source != KnowledgeSourceLocal {
		t.Fatalf("expected local source, got %q", row.Source)
	}
	files := row.Files()
	if len(files) != 2 || files[0] != "data/**/*.jsonl" {
		t.Fatalf("unexpected files: %v", files)
	}
}
