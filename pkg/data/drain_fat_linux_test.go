//go:build linux && !cgo

package data

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadyCurrentRuntimeUsesCurrentBinaryWhenHashMatches(t *testing.T) {
	t.Parallel()

	hash := strings.Repeat("ab", 32)
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	bundle := filepath.Join(dataRoot, hash)
	writeReadyBundleDir(t, bundle)
	if err := os.Symlink(hash, filepath.Join(dataRoot, "current")); err != nil {
		t.Fatalf("symlink current: %v", err)
	}

	got, ok, err := ReadyCurrentRuntime(root, hash)
	if err != nil {
		t.Fatalf("ReadyCurrentRuntime: %v", err)
	}
	if !ok {
		t.Fatal("expected ready current runtime")
	}
	want := filepath.Join(dataRoot, "current", "bin", "edgelet")
	if got != want {
		t.Fatalf("path=%q want %q", got, want)
	}
}

func TestReadyCurrentRuntimeSkipsHashMismatchWithoutPromote(t *testing.T) {
	t.Parallel()

	installed := strings.Repeat("ab", 32)
	embedded := strings.Repeat("cd", 32)
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	writeReadyBundleDir(t, filepath.Join(dataRoot, installed))
	if err := os.Symlink(installed, filepath.Join(dataRoot, "current")); err != nil {
		t.Fatalf("symlink current: %v", err)
	}
	before, err := os.Readlink(filepath.Join(dataRoot, "current"))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}

	_, ok, err := ReadyCurrentRuntime(root, embedded)
	if err != nil {
		t.Fatalf("ReadyCurrentRuntime: %v", err)
	}
	if ok {
		t.Fatal("hash mismatch must not select data/current")
	}
	after, err := os.Readlink(filepath.Join(dataRoot, "current"))
	if err != nil {
		t.Fatalf("readlink after: %v", err)
	}
	if after != before {
		t.Fatalf("current symlink changed from %q to %q", before, after)
	}
	if _, err := os.Stat(filepath.Join(dataRoot, embedded)); !os.IsNotExist(err) {
		t.Fatalf("mismatch must not create data/%s: %v", embedded, err)
	}
}

func TestStageFatELFDoesNotPromoteCurrent(t *testing.T) {
	t.Parallel()

	hash := strings.Repeat("ab", 32)
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	writeReadyBundleDir(t, filepath.Join(dataRoot, hash))
	current := filepath.Join(dataRoot, "current")
	if err := os.Symlink(hash, current); err != nil {
		t.Fatalf("symlink current: %v", err)
	}
	installed := filepath.Join(root, "edgelet-installed")
	if err := os.WriteFile(installed, []byte("installed-binary"), 0o755); err != nil {
		t.Fatalf("write installed: %v", err)
	}

	payload := []byte("\x7fELFfat-runtime")
	blob := zstdTarForTest(t, map[string][]byte{
		"bin/edgelet":         payload,
		"bin/containerd-shim": []byte("shim"),
		"images/pause.tar.gz": []byte("pause"),
	})
	stageDir := filepath.Join(root, "runtime-drain")
	dest := filepath.Join(stageDir, "edgelet")
	if err := stageFatELFFromBundle(bytes.NewReader(blob), dest); err != nil {
		t.Fatalf("stageFatELFFromBundle: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read staged elf: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("staged bytes = %q", got)
	}
	if _, err := os.Stat(filepath.Join(stageDir, "containerd-shim")); !os.IsNotExist(err) {
		t.Fatalf("staged tree must not include other bundle files: %v", err)
	}
	link, err := os.Readlink(current)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if link != hash {
		t.Fatalf("current symlink = %q want %q", link, hash)
	}
	still, err := os.ReadFile(installed)
	if err != nil {
		t.Fatalf("read installed: %v", err)
	}
	if string(still) != "installed-binary" {
		t.Fatalf("installed binary changed: %q", still)
	}
}

func zstdTarForTest(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	return mustZstdTar(t, files)
}
