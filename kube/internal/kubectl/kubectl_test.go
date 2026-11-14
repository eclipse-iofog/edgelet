package kubectl

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet-kube/scripts/airgap"
)

func TestVersionReportsPin(t *testing.T) {
	stdout, stderr := capture(t, func() {
		if err := Run([]string{"--data-dir", t.TempDir(), "version", "--client=true"}); err != nil {
			t.Fatal(err)
		}
	})
	got := stdout + stderr
	if !strings.Contains(got, airgap.KubernetesVersion()) {
		t.Fatalf("kubectl version\n%s", got)
	}
}

func TestDataDirFlagIsRemoved(t *testing.T) {
	got := withoutRuntimeFlags([]string{"--data-dir", "/var/lib/edgelet-kube", "version", "--client=true"})
	if strings.Join(got, " ") != "version --client=true" {
		t.Fatalf("args %v", got)
	}
	got = withoutRuntimeFlags([]string{"--data-dir=/tmp/work", "get", "nodes"})
	if strings.Join(got, " ") != "get nodes" {
		t.Fatalf("args %v", got)
	}
}

func TestAdminKubeconfigIsUsedWhenUnset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edgelet-kube.yaml")
	if err := os.WriteFile(path, []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := adminKubeconfig
	adminKubeconfig = path
	t.Cleanup(func() { adminKubeconfig = old })
	t.Setenv("KUBECONFIG", "")
	if err := useAdminKubeconfig([]string{"get", "nodes"}); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("KUBECONFIG") != path {
		t.Fatalf("KUBECONFIG %s", os.Getenv("KUBECONFIG"))
	}
}

func TestKubeconfigFlagAndEnvWin(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edgelet-kube.yaml")
	if err := os.WriteFile(path, []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := adminKubeconfig
	adminKubeconfig = path
	t.Cleanup(func() { adminKubeconfig = old })

	t.Setenv("KUBECONFIG", "")
	if err := useAdminKubeconfig([]string{"--kubeconfig", "/tmp/other", "get", "nodes"}); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("KUBECONFIG") != "" {
		t.Fatalf("KUBECONFIG %s", os.Getenv("KUBECONFIG"))
	}
	if err := useAdminKubeconfig([]string{"--kubeconfig=/tmp/other", "get", "nodes"}); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("KUBECONFIG") != "" {
		t.Fatalf("KUBECONFIG %s", os.Getenv("KUBECONFIG"))
	}

	t.Setenv("KUBECONFIG", "/tmp/already")
	if err := useAdminKubeconfig([]string{"get", "nodes"}); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("KUBECONFIG") != "/tmp/already" {
		t.Fatalf("KUBECONFIG %s", os.Getenv("KUBECONFIG"))
	}
}

func capture(t *testing.T, fn func()) (string, string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	fn()
	_ = outW.Close()
	_ = errW.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	var stdout, stderr bytes.Buffer
	if _, err := io.Copy(&stdout, outR); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(&stderr, errR); err != nil {
		t.Fatal(err)
	}
	return stdout.String(), stderr.String()
}
