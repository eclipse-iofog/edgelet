//go:build linux

package runtimecmd

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/pkg/containerd"
)

func TestReapOrphans_SkipsWithoutVerifiedDrain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "drain-verified")
	prev := containerd.DrainVerifiedMarkerPath()
	containerd.SetDrainVerifiedMarkerPath(path)
	t.Cleanup(func() { containerd.SetDrainVerifiedMarkerPath(prev) })
	_ = containerd.ClearDrainVerifiedMarker()

	reapCalled := false
	prevReap := reapManagedShimsUntilClear
	reapManagedShimsUntilClear = func(string, time.Duration) error {
		reapCalled = true
		return nil
	}
	t.Cleanup(func() { reapManagedShimsUntilClear = prevReap })

	result, err := ReapOrphans()
	if reapCalled {
		t.Fatal("must not reap shims when drain did not verify")
	}
	if err != nil {
		t.Fatalf("ReapOrphans: %v", err)
	}
	if result == nil || result.Data["status"] != "skipped" {
		t.Fatalf("expected skipped reap when drain did not verify, got %+v", result)
	}
}

func TestReapOrphans_RunsAfterVerifiedDrain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "drain-verified")
	prev := containerd.DrainVerifiedMarkerPath()
	containerd.SetDrainVerifiedMarkerPath(path)
	t.Cleanup(func() { containerd.SetDrainVerifiedMarkerPath(prev) })

	if err := containerd.WriteDrainVerifiedMarker(); err != nil {
		t.Fatalf("WriteDrainVerifiedMarker: %v", err)
	}

	reapCalled := false
	prevReap := reapManagedShimsUntilClear
	reapManagedShimsUntilClear = func(string, time.Duration) error {
		reapCalled = true
		return nil
	}
	t.Cleanup(func() { reapManagedShimsUntilClear = prevReap })

	result, err := ReapOrphans()
	if err != nil {
		t.Fatalf("ReapOrphans after verified drain: %v", err)
	}
	if !reapCalled {
		t.Fatal("expected child/shim cleanup after verified drain")
	}
	if result == nil || result.Data["status"] != "complete" {
		t.Fatalf("expected orphan reap after verified drain, got %+v", result)
	}
}
