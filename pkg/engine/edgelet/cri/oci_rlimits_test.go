//go:build linux

package cri

import (
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/models"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

func TestApplyOCIRlimits(t *testing.T) {
	ms := models.NewMicroservice("ms-1", "alpine:3.19")
	ms.Ulimits = map[string]models.Ulimit{
		"nofile": {Soft: 1024, Hard: 2048},
	}
	spec := &specs.Spec{Process: &specs.Process{}}
	ApplyOCIRlimits(spec, ms)
	if len(spec.Process.Rlimits) != 1 {
		t.Fatalf("rlimits = %d", len(spec.Process.Rlimits))
	}
	if spec.Process.Rlimits[0].Type != "RLIMIT_NOFILE" {
		t.Fatalf("type = %s", spec.Process.Rlimits[0].Type)
	}
	if spec.Process.Rlimits[0].Soft != 1024 || spec.Process.Rlimits[0].Hard != 2048 {
		t.Fatalf("rlimit = %+v", spec.Process.Rlimits[0])
	}
}
