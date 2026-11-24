package version

import (
	"testing"

	"github.com/eclipse-iofog/edgelet-kube/scripts/airgap"
	clientversion "k8s.io/client-go/pkg/version"
	baseversion "k8s.io/component-base/version"
)

func TestGitVersionMatchesPin(t *testing.T) {
	want := airgap.KubernetesVersion()
	if got := GitVersion(); got != want {
		t.Fatalf("component-base gitVersion %s, pin %s", got, want)
	}
	if got := baseversion.Get().GitVersion; got != want {
		t.Fatalf("Get().GitVersion %s", got)
	}
	major, minor := majorMinor(want)
	if got := baseversion.Get().Major; got != major {
		t.Fatalf("major %s, pin %s", got, major)
	}
	if got := baseversion.Get().Minor; got != minor {
		t.Fatalf("minor %s, pin %s", got, minor)
	}
	if got := clientversion.Get().GitVersion; got != want {
		t.Fatalf("client gitVersion %s", got)
	}
}
