package airgap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKubernetesVersion(t *testing.T) {
	if got := KubernetesVersion(); got != "v1.37.2" {
		t.Fatalf("kubernetes version %q", got)
	}
}

func TestModuleMatchesKubernetesVersion(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(kubeRoot(t), "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	want := "k8s.io/kubernetes " + KubernetesVersion()
	if !strings.Contains(string(body), want) {
		t.Fatalf("go.mod does not require %s", want)
	}
}
