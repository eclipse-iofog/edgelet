//go:build linux

package cri

import (
	"github.com/eclipse-iofog/edgelet/internal/containerapply"
	"github.com/eclipse-iofog/edgelet/internal/models"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// ApplyOCIRlimits writes POSIX rlimits onto the OCI process spec.
func ApplyOCIRlimits(spec *specs.Spec, ms *models.Microservice) {
	if spec == nil || ms == nil || len(ms.Ulimits) == 0 {
		return
	}
	if spec.Process == nil {
		spec.Process = &specs.Process{}
	}
	spec.Process.Rlimits = containerapply.POSIXRlimits(ms.Ulimits)
}
