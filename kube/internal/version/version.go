// Package version sets the Kubernetes version reported by this binary.
package version

import (
	"strings"
	"sync/atomic"
	_ "unsafe" // go:linkname

	"github.com/eclipse-iofog/edgelet-kube/scripts/airgap"
	_ "k8s.io/client-go/pkg/version" // go:linkname client gitVersion
	baseversion "k8s.io/component-base/version"
)

//go:linkname dynamicGitVersion k8s.io/component-base/version.dynamicGitVersion
var dynamicGitVersion atomic.Value

//go:linkname gitVersion k8s.io/component-base/version.gitVersion
var gitVersion string

//go:linkname gitMajor k8s.io/component-base/version.gitMajor
var gitMajor string

//go:linkname gitMinor k8s.io/component-base/version.gitMinor
var gitMinor string

//go:linkname clientGitVersion k8s.io/client-go/pkg/version.gitVersion
var clientGitVersion string

//go:linkname clientGitMajor k8s.io/client-go/pkg/version.gitMajor
var clientGitMajor string

//go:linkname clientGitMinor k8s.io/client-go/pkg/version.gitMinor
var clientGitMinor string

func init() {
	Pin()
}

// Pin sets component-base gitVersion from the pinned Kubernetes patch.
func Pin() {
	pinned := airgap.KubernetesVersion()
	if pinned == "" {
		panic("kubernetes version is not set")
	}
	major, minor := majorMinor(pinned)
	dynamicGitVersion.Store(pinned)
	gitVersion = pinned
	gitMajor = major
	gitMinor = minor
	clientGitVersion = pinned
	clientGitMajor = major
	clientGitMinor = minor
}

// GitVersion is the Kubernetes patch reported by component-base.
func GitVersion() string {
	return baseversion.Get().GitVersion
}

func majorMinor(pinned string) (string, string) {
	rest := strings.TrimPrefix(pinned, "v")
	major, rest, _ := strings.Cut(rest, ".")
	minor, _, _ := strings.Cut(rest, ".")
	return major, minor
}
