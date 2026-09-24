package modelpull

import (
	"strings"
	"testing"
)

func TestEnsureDiskSpace_RejectsWhenOverThreshold(t *testing.T) {
	err := EnsureDiskSpace(50, 100, 1000, 20, "/var/lib/edgelet")
	if err == nil || !strings.Contains(err.Error(), "available-disk threshold") {
		t.Fatalf("expected threshold rejection, got %v", err)
	}
	if !strings.Contains(err.Error(), "/var/lib/edgelet") || !strings.Contains(err.Error(), "50") {
		t.Fatalf("error should name the path and size, got %v", err)
	}
}

func TestEnsureDiskSpace_RejectsWhenCannotFit(t *testing.T) {
	err := EnsureDiskSpace(200, 100, 1000, 20, "/data")
	if err == nil || !strings.Contains(err.Error(), "have 100 bytes free") {
		t.Fatalf("expected capacity rejection, got %v", err)
	}
}

func TestEnsureDiskSpace_AllowsWhenRemainingMeetsThreshold(t *testing.T) {
	if err := EnsureDiskSpace(50, 300, 1000, 20, "/data"); err != nil {
		t.Fatalf("expected allow, got %v", err)
	}
}

func TestEnsureDiskSpace_SkipsUnknownSize(t *testing.T) {
	if err := EnsureDiskSpace(0, 1, 1000, 90, "/data"); err != nil {
		t.Fatalf("unknown size must not fail: %v", err)
	}
}
