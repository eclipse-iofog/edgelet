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
	if got := baseversion.Get().Major; got != "1" {
		t.Fatalf("major %s", got)
	}
	if got := baseversion.Get().Minor; got != "36" {
		t.Fatalf("minor %s", got)
	}
	if got := clientversion.Get().GitVersion; got != want {
		t.Fatalf("client gitVersion %s", got)
	}
}
