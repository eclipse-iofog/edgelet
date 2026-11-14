package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConformanceJob(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "conformance-kube.yml"))
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(filepath.Join(root, "kube", "scripts", "conformance.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(workflow)
	for _, want := range []string{
		"certified-conformance",
		"edgelet-kube-linux-amd64",
		"edgelet-kube-airgap-images-amd64.tar.zst",
		"runs-on: ubuntu-latest",
		"timeout-minutes: 240",
		`CONFORMANCE_TIMEOUT_MINUTES: "240"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("job definition missing %s", want)
		}
	}
	if strings.Contains(text, "--disable") || strings.Contains(text, "\ndisable:") {
		t.Fatal("job definition turns an addon off")
	}
	body := string(script)
	for _, want := range []string{
		"--mode=certified-conformance",
		"edgelet-kube-linux-amd64",
		"edgelet-kube-airgap-images-amd64.tar.zst",
		"CONFORMANCE_TIMEOUT_MINUTES:-240",
		"reserve_minutes=45",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("conformance script missing %s", want)
		}
	}
	if strings.Contains(body, "--disable") {
		t.Fatal("conformance script turns an addon off")
	}
}
