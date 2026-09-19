package edgelet

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscardNamedVolumeDoesNotDeleteVolumeTrees(t *testing.T) {
	base := t.TempDir()
	wrong := filepath.Join(base, "volumes", "iofog-controller-db", "marker")
	dataPath := filepath.Join(base, "volumes", "data", "cp-uuid", "iofog-controller-db", "marker")
	for _, marker := range []string{wrong, dataPath} {
		if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := discardNamedVolume("iofog-controller-db"); err != nil {
		t.Fatalf("named volume removal: %v", err)
	}

	for _, marker := range []string{wrong, dataPath} {
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("named volume removal must not delete %s: %v", marker, err)
		}
	}
}

func TestDiscardNamedVolumeRequiresName(t *testing.T) {
	if err := discardNamedVolume("  "); err == nil {
		t.Fatal("expected error for empty volume name")
	}
}
