//go:build linux

package cdidevices

import (
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/constants"
)

var containerdConfigDDir = constants.EdgeletContainerdLibDir + "/config.d"

// ListAvailable returns fully-qualified CDI device names for the edgelet engine.
// Docker, podman, and other engines return an empty list.
func ListAvailable(engineName string) []string {
	if !strings.EqualFold(strings.TrimSpace(engineName), constants.EngineEdgelet) {
		return []string{}
	}
	dirs := append([]string{}, DefaultSpecDirs...)
	dirs = append(dirs, ExtraSpecDirsFromConfigD(containerdConfigDDir)...)
	return ListFromDirs(dirs)
}
