package cdidevices

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestListFromDirs_FQNamesFromDefaultAndExtraDirs(t *testing.T) {
	root := t.TempDir()
	etc := filepath.Join(root, "etc-cdi")
	run := filepath.Join(root, "run-cdi")
	extra := filepath.Join(root, "vendor-cdi")
	missing := filepath.Join(root, "missing")
	for _, dir := range []string{etc, run, extra} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	writeFile(t, filepath.Join(etc, "nvidia.json"), `{
		"cdiVersion": "0.6.0",
		"kind": "nvidia.com/gpu",
		"devices": [{"name": "0"}, {"name": "1"}]
	}`)
	writeFile(t, filepath.Join(run, "webgpu.yaml"), "kind: docker.com/gpu\ndevices:\n  - name: webgpu\n")
	writeFile(t, filepath.Join(extra, "npu.json"), `{
		"kind": "vendor.com/npu",
		"devices": [{"name": "0"}]
	}`)
	writeFile(t, filepath.Join(etc, "ignored.txt"), "not a spec")
	writeFile(t, filepath.Join(etc, "subdir-placeholder.json"), `{"kind":"","devices":[]}`)

	got := ListFromDirs([]string{etc, run, extra, missing, etc})
	want := []string{
		"docker.com/gpu=webgpu",
		"nvidia.com/gpu=0",
		"nvidia.com/gpu=1",
		"vendor.com/npu=0",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestListFromDirs_EmptyWhenNothingFound(t *testing.T) {
	got := ListFromDirs([]string{filepath.Join(t.TempDir(), "nope")})
	if got == nil || len(got) != 0 {
		t.Fatalf("expected empty slice, got %#v", got)
	}
}

func TestExtraSpecDirsFromConfigD(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "10-vendor.toml"), `
[plugins."io.containerd.cri.v1.runtime"]
  cdi_spec_dirs = ["/etc/cdi", "/var/run/cdi", "/opt/vendor/cdi"]
`)
	writeFile(t, filepath.Join(dir, "20-dup.toml"), `
cdi_spec_dirs = ["/opt/vendor/cdi", "/opt/other/cdi"]
`)
	writeFile(t, filepath.Join(dir, "notes.txt"), `cdi_spec_dirs = ["/ignored"]`)
	got := ExtraSpecDirsFromConfigD(dir)
	want := []string{"/etc/cdi", "/var/run/cdi", "/opt/vendor/cdi", "/opt/other/cdi"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
