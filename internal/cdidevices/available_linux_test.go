//go:build linux

package cdidevices

import (
	"reflect"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/constants"
)

func TestListAvailable_DockerAndPodmanEmpty(t *testing.T) {
	if got := ListAvailable(constants.EngineDocker); !reflect.DeepEqual(got, []string{}) {
		t.Fatalf("docker: %#v", got)
	}
	if got := ListAvailable(constants.EnginePodman); !reflect.DeepEqual(got, []string{}) {
		t.Fatalf("podman: %#v", got)
	}
}
