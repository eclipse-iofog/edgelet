package runtimeapi

import (
	"fmt"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

func TestFacadeDeleteRegistry_RefusesBuiltIns(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })
	if err := f.db.EnsureDefaultLocalRegistries(); err != nil {
		t.Fatalf("seed registries: %v", err)
	}

	for _, id := range []int{
		models.BuiltInRegistryDockerIO,
		models.BuiltInRegistryFromCache,
		models.BuiltInRegistryHuggingFace,
	} {
		err := f.DeleteRegistry(id)
		if err == nil || !strings.Contains(err.Error(), "cannot be removed") {
			t.Fatalf("expected built-in %d delete refusal, got: %v", id, err)
		}
	}
}

func TestFacadeApplyLocalRegistryManifest_RefusesBuiltInIDs(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })

	cases := []struct {
		id  int
		typ string
		url string
	}{
		{models.BuiltInRegistryDockerIO, models.RegistryTypeOCI, "docker.io"},
		{models.BuiltInRegistryFromCache, models.RegistryTypeOCI, "from_cache"},
		{models.BuiltInRegistryHuggingFace, models.RegistryTypeHF, models.DefaultHuggingFaceHubURL},
	}
	for _, tc := range cases {
		manifest := fmt.Sprintf(`
apiVersion: edgelet.iofog.org/v1
kind: Registry
spec:
  id: %d
  type: %s
  url: %s
  private: false
`, tc.id, tc.typ, tc.url)
		_, err := f.ApplyLocalRegistryManifest(manifest, true)
		if err == nil || !strings.Contains(err.Error(), "cannot be edited") {
			t.Fatalf("expected built-in %d apply refusal (dry-run), got: %v", tc.id, err)
		}
		_, err = f.ApplyLocalRegistryManifest(manifest, false)
		if err == nil || !strings.Contains(err.Error(), "cannot be edited") {
			t.Fatalf("expected built-in %d apply refusal, got: %v", tc.id, err)
		}
	}
}

func TestFacadeApplyLocalRegistryManifest_AllocatesAfterBuiltIns(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })

	manifest := `
apiVersion: edgelet.iofog.org/v1
kind: Registry
spec:
  type: oci
  url: quay.io
  private: false
`
	reg, err := f.ApplyLocalRegistryManifest(manifest, false)
	if err != nil {
		t.Fatalf("apply user registry: %v", err)
	}
	if reg.ID != models.HighestBuiltInLocalRegistryID()+1 {
		t.Fatalf("expected first user registry id %d, got %d", models.HighestBuiltInLocalRegistryID()+1, reg.ID)
	}
	if err := f.DeleteRegistry(reg.ID); err != nil {
		t.Fatalf("delete user registry: %v", err)
	}
}
