package stage

import (
	"fmt"
	"strings"

	"github.com/eclipse-iofog/edgelet-kube/scripts/airgap"
)

// imageRef is the pinned reference for repo, prefixed when registry is set.
// repo is the repository path without a registry, such as coredns/coredns.
func imageRef(registry, repo string) (string, error) {
	var match string
	for _, ref := range airgap.Images() {
		name, _, _ := strings.Cut(ref, "@")
		got, _, ok := strings.Cut(name, ":")
		if !ok {
			continue
		}
		if got != repo && !strings.HasSuffix(got, "/"+repo) {
			continue
		}
		if match != "" && match != ref {
			return "", fmt.Errorf("more than one pinned image matches %s", repo)
		}
		match = ref
	}
	if match == "" {
		return "", fmt.Errorf("pinned image %s is not on the list", repo)
	}
	return airgap.Resolve(registry, match), nil
}
