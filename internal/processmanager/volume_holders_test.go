package processmanager

import (
	"path/filepath"
	"testing"
)

func TestPersistentVolumeTrees(t *testing.T) {
	got := PersistentVolumeTrees("/var/lib/edgelet")
	if len(got) != 2 {
		t.Fatalf("expected data and shared trees, got %v", got)
	}
	if got[0] != filepath.Join("/var/lib/edgelet", "volumes", "data") {
		t.Fatalf("unexpected data tree %q", got[0])
	}
	if got[1] != filepath.Join("/var/lib/edgelet", "volumes", "shared") {
		t.Fatalf("unexpected shared tree %q", got[1])
	}
	if PersistentVolumeTrees("  ") != nil {
		t.Fatal("empty disk directory must yield no trees")
	}
}

func TestPathHoldsVolumeTree(t *testing.T) {
	trees := PersistentVolumeTrees("/var/lib/edgelet")
	if !PathHoldsVolumeTree("/var/lib/edgelet/volumes/data/uuid/status", trees) {
		t.Fatal("expected flock path under volumes/data to match")
	}
	if !PathHoldsVolumeTree("/var/lib/edgelet/volumes/shared/name/file", trees) {
		t.Fatal("expected path under volumes/shared to match")
	}
	if PathHoldsVolumeTree("/var/lib/edgelet/volumes/microservices/x", trees) {
		t.Fatal("must not treat microservice bind trees as VOLUME holders")
	}
	if PathHoldsVolumeTree("socket:[123]", trees) {
		t.Fatal("must not treat socket targets as VOLUME holders")
	}
}
