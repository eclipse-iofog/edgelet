package airgap

import (
	"slices"
	"strings"
	"testing"
)

func TestImagesForRISCVOmitsUnpublishedAddons(t *testing.T) {
	if len(Images()) != 12 {
		t.Fatalf("pin list %d", len(Images()))
	}
	for _, arch := range []string{"amd64", "arm64", "arm"} {
		if !slices.Equal(ImagesFor(arch), Images()) {
			t.Fatalf("%s list changed", arch)
		}
	}
	got := ImagesFor("riscv64")
	if len(got) != 6 {
		t.Fatalf("riscv64 list %d: %v", len(got), got)
	}
	if !strings.HasPrefix(got[0], "docker.io/portainer/pause:latest@sha256:df6d2fb5") {
		t.Fatalf("pause %s", got[0])
	}
	for _, ref := range got {
		if strings.Contains(ref, "metrics-server") || strings.Contains(ref, "cert-manager") {
			t.Fatalf("riscv64 list contains %s", ref)
		}
	}
	var omitted int
	for _, ref := range Images() {
		if !slices.Contains(got, ref) {
			omitted++
		}
	}
	if omitted != 6 {
		t.Fatalf("omitted %d", omitted)
	}
}
