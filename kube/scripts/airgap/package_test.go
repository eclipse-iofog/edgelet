package airgap

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPackageScriptWritesPinList(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64", "arm", "riscv64"} {
		t.Run(arch, func(t *testing.T) {
			packageScriptWritesPinList(t, arch)
		})
	}
}

func packageScriptWritesPinList(t *testing.T, arch string) {
	t.Helper()
	root := kubeRoot(t)
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "images.tar")
	wantImages := ImagesFor(arch)
	var raw bytes.Buffer
	if err := WriteTar(&raw, wantImages); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tarPath, raw.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "ctr.log")
	ctrPath := filepath.Join(dir, "ctr")
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$CTR_LOG"
if [ "$1" = "namespaces" ]; then
  exit 0
fi
if [ "$4" = "pull" ]; then
  exit 0
fi
if [ "$4" = "export" ]; then
  cp "$CTR_TAR" "$6"
  exit 0
fi
echo "unexpected: $*" >&2
exit 1
`
	if err := os.WriteFile(ctrPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "edgelet-kube-airgap-images-"+arch+".tar.zst")
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "package-airgap"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"ARCH="+arch,
		"OUT="+out,
		"CTR="+ctrPath,
		"CTR_LOG="+logPath,
		"CTR_TAR="+tarPath,
		"GOWORK=off",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("package-airgap: %v\n%s", err, output)
	}
	names, err := ListFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(names, wantImages) {
		t.Fatalf("archive %v", names)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	assertCtrExportsPinList(t, string(log), arch)
}

func TestPackageScriptRefusesOtherArch(t *testing.T) {
	root := kubeRoot(t)
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "package-airgap"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "ARCH=ppc64le", "GOWORK=off")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("unsupported architecture was built")
	}
	if !strings.Contains(string(output), "unsupported architecture") {
		t.Fatalf("output %s", output)
	}
}

func TestPackageScriptSkipsRuntimeEmbed(t *testing.T) {
	root := kubeRoot(t)
	out := filepath.Join(root, "cmd", "edgelet-kube", "embed", AssetAMD64)
	t.Cleanup(func() {
		_ = os.Remove(out)
	})
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "package-airgap"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "ARCH=amd64", "OUT="+out, "GOWORK=off")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("archive was written into the runtime")
	}
	if !strings.Contains(string(output), "beside the binary") {
		t.Fatalf("output %s", output)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestRuntimeBuildOmitsAirgapAsset(t *testing.T) {
	root := kubeRoot(t)
	for _, rel := range []string{
		"scripts/build.sh",
		"scripts/pack/main.go",
		"scripts/bundle-stage.sh",
		"cmd/edgelet-kube/embed.go",
		"cmd/edgelet-kube/extract.go",
		"internal/bundle/archive.go",
	} {
		body, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), AssetAMD64) {
			t.Fatalf("%s links the airgap archive", rel)
		}
	}
	matches, err := filepath.Glob(filepath.Join(root, "cmd", "edgelet-kube", "embed", "*airgap*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatal(matches)
	}
}

func assertCtrExportsPinList(t *testing.T, log, arch string) {
	t.Helper()
	platform := "--platform=linux/" + arch
	if arch == "arm" {
		platform = "--platform=linux/arm/v7"
	}
	var pulled []string
	var exported []string
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		fields := strings.Fields(line)
		switch {
		case len(fields) >= 4 && fields[0] == "-n" && fields[1] == "k8s.io" && fields[2] == "images" && fields[3] == "pull":
			if fields[4] != platform {
				t.Fatalf("pull %s", line)
			}
			pulled = append(pulled, fields[len(fields)-1])
		case len(fields) >= 6 && fields[0] == "-n" && fields[1] == "k8s.io" && fields[2] == "images" && fields[3] == "export":
			if fields[4] != platform {
				t.Fatalf("export %s", line)
			}
			exported = fields[6:]
		}
	}
	want := ImagesFor(arch)
	if !slices.Equal(pulled, want) {
		t.Fatalf("pulled %v", pulled)
	}
	if !slices.Equal(exported, want) {
		t.Fatalf("exported %v", exported)
	}
}

func kubeRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}
