package store

import (
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

func TestEnsureDefaultControllerRegistries(t *testing.T) {
	db := GetInstance()
	if err := db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.EnsureDefaultControllerRegistries(); err != nil {
		t.Fatalf("EnsureDefaultControllerRegistries failed: %v", err)
	}
	registries, err := db.LoadControllerRegistries()
	if err != nil {
		t.Fatalf("LoadControllerRegistries failed: %v", err)
	}

	foundDockerIO := false
	foundFromCache := false
	for _, reg := range registries {
		if reg.ID == models.BuiltInRegistryHuggingFace {
			t.Fatalf("controller defaults must not include Hugging Face Hub, got %+v", registries)
		}
		if reg.ID == models.BuiltInRegistryDockerIO && reg.URL == "docker.io" && reg.Type == models.RegistryTypeOCI {
			foundDockerIO = true
		}
		if reg.ID == models.BuiltInRegistryFromCache && reg.URL == "from_cache" && reg.Type == models.RegistryTypeOCI {
			foundFromCache = true
		}
	}
	if !foundDockerIO || !foundFromCache {
		t.Fatalf("expected default registries to exist, got %+v", registries)
	}
}

func TestEnsureDefaultLocalRegistries_SeedsHuggingFaceHub(t *testing.T) {
	db := openFreshStoreDB(t)
	if err := db.EnsureDefaultLocalRegistries(); err != nil {
		t.Fatalf("EnsureDefaultLocalRegistries failed: %v", err)
	}
	registries, err := db.LoadLocalRegistries()
	if err != nil {
		t.Fatalf("LoadLocalRegistries failed: %v", err)
	}
	if len(registries) < 3 {
		t.Fatalf("expected at least 3 built-in local registries, got %+v", registries)
	}

	foundDockerIO := false
	foundFromCache := false
	foundHF := false
	for _, reg := range registries {
		switch reg.ID {
		case models.BuiltInRegistryDockerIO:
			if reg.URL != "docker.io" || reg.Type != models.RegistryTypeOCI || !reg.IsPublic || reg.Insecure || reg.CAB64 != "" {
				t.Fatalf("unexpected docker.io built-in: %+v", reg)
			}
			foundDockerIO = true
		case models.BuiltInRegistryFromCache:
			if reg.URL != "from_cache" || reg.Type != models.RegistryTypeOCI || !reg.IsPublic {
				t.Fatalf("unexpected from_cache built-in: %+v", reg)
			}
			foundFromCache = true
		case models.BuiltInRegistryHuggingFace:
			if reg.URL != models.DefaultHuggingFaceHubURL || reg.Type != models.RegistryTypeHF || !reg.IsPublic || reg.Insecure || reg.CAB64 != "" {
				t.Fatalf("unexpected Hugging Face built-in: %+v", reg)
			}
			foundHF = true
		}
	}
	if !foundDockerIO || !foundFromCache || !foundHF {
		t.Fatalf("expected docker.io, from_cache, and Hugging Face Hub, got %+v", registries)
	}
}
