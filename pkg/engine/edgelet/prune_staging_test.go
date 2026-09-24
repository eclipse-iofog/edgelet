package edgelet

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRebuildableVolumeStagingTargets_SkipsPersistentVolumeTrees(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data", "ms-uuid")
	sharedDir := filepath.Join(base, "shared", "shared-name")
	keepStaging := filepath.Join(base, "microservices", "keep-uuid")
	dropStaging := filepath.Join(base, "microservices", "drop-uuid")
	for _, dir := range []string{dataDir, sharedDir, keepStaging, dropStaging} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	targets, err := rebuildableVolumeStagingTargets(base, map[string]struct{}{"keep-uuid": {}})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0] != dropStaging {
		t.Fatalf("expected only rebuildable staging %q, got %v", dropStaging, targets)
	}
}

func TestPruneVolumesTargetsSkipPersistentVolumeTrees(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data", "ms-uuid", "name")
	sharedDir := filepath.Join(base, "shared", "shared-name")
	for _, dir := range []string{dataDir, sharedDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	targets, err := rebuildableVolumeStagingTargets(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 0 {
		t.Fatalf("empty container list must not prune persistent volume trees, got %v", targets)
	}
	if _, err := os.Stat(dataDir); err != nil {
		t.Fatalf("data tree missing: %v", err)
	}
	if _, err := os.Stat(sharedDir); err != nil {
		t.Fatalf("shared tree missing: %v", err)
	}
}

func TestPruneRebuildableVolumeStaging_LeavesPersistentVolumeMarkers(t *testing.T) {
	base := t.TempDir()
	dataMarker := filepath.Join(base, "data", "ms-uuid", "name", "marker")
	sharedMarker := filepath.Join(base, "shared", "shared-name", "marker")
	dropStaging := filepath.Join(base, "microservices", "drop-uuid")
	for _, dir := range []string{filepath.Dir(dataMarker), filepath.Dir(sharedMarker), dropStaging} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(dataMarker, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sharedMarker, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	targets, err := rebuildableVolumeStagingTargets(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if err := os.RemoveAll(target); err != nil {
			t.Fatal(err)
		}
	}

	for _, marker := range []string{dataMarker, sharedMarker} {
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("persistent volume marker missing: %s: %v", marker, err)
		}
	}
	if _, err := os.Stat(dropStaging); !os.IsNotExist(err) {
		t.Fatalf("expected rebuildable staging to be removed, err=%v", err)
	}
}
