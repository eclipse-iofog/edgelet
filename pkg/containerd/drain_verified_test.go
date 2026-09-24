package containerd

import (
	"path/filepath"
	"testing"
)

func TestDrainVerifiedMarker_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "drain-verified")
	prev := DrainVerifiedMarkerPath()
	SetDrainVerifiedMarkerPath(path)
	t.Cleanup(func() { SetDrainVerifiedMarkerPath(prev) })

	if HasDrainVerifiedMarker() {
		t.Fatal("expected no drain-verified marker before write")
	}
	if err := WriteDrainVerifiedMarker(); err != nil {
		t.Fatalf("WriteDrainVerifiedMarker: %v", err)
	}
	if !HasDrainVerifiedMarker() {
		t.Fatal("expected drain-verified marker after write")
	}
	if err := ClearDrainVerifiedMarker(); err != nil {
		t.Fatalf("ClearDrainVerifiedMarker: %v", err)
	}
	if HasDrainVerifiedMarker() {
		t.Fatal("expected marker cleared")
	}
}
