package supervisor

import (
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/store"
)

func TestEnsureDefaultLocalRegistriesOnStartup_SeedsDefaults(t *testing.T) {
	db := store.GetInstance()
	_ = db.Close()
	if err := db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	s := NewSupervisor()
	if err := s.ensureDefaultLocalRegistriesOnStartup(db); err != nil {
		t.Fatalf("ensureDefaultLocalRegistriesOnStartup failed: %v", err)
	}

	registries, err := db.LoadLocalRegistries()
	if err != nil {
		t.Fatalf("LoadLocalRegistries failed: %v", err)
	}
	foundDockerIO := false
	foundFromCache := false
	foundHF := false
	for _, reg := range registries {
		if reg.ID == models.BuiltInRegistryDockerIO && reg.URL == "docker.io" {
			foundDockerIO = true
		}
		if reg.ID == models.BuiltInRegistryFromCache && reg.URL == "from_cache" {
			foundFromCache = true
		}
		if reg.ID == models.BuiltInRegistryHuggingFace &&
			reg.URL == models.DefaultHuggingFaceHubURL &&
			reg.Type == models.RegistryTypeHF {
			foundHF = true
		}
	}
	if !foundDockerIO || !foundFromCache || !foundHF {
		t.Fatalf("expected default local registries to exist, got %+v", registries)
	}
}
