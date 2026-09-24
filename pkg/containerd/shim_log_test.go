package containerd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareWasmShimLogDirMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shim-log")
	if err := prepareWasmShimLog(dir); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		t.Fatalf("expected a real directory, mode %s", info.Mode())
	}
	if info.Mode().Perm() != 0o750 {
		t.Fatalf("directory mode %o, want 0750", info.Mode().Perm())
	}
	logInfo, err := os.Lstat(filepath.Join(dir, wasmShimLogFile))
	if err != nil {
		t.Fatal(err)
	}
	if logInfo.Mode()&os.ModeSymlink != 0 || !logInfo.Mode().IsRegular() {
		t.Fatalf("expected a regular log file, mode %s", logInfo.Mode())
	}
}

func TestPrepareWasmShimLogRejectsDirectorySymlink(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "dest")
	if err := os.Mkdir(dest, 0o750); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(dest, link); err != nil {
		t.Fatal(err)
	}
	if err := prepareWasmShimLog(link); err == nil {
		t.Fatal("expected symlink directory to be rejected")
	}
	if _, err := os.Lstat(filepath.Join(dest, wasmShimLogFile)); !os.IsNotExist(err) {
		t.Fatalf("symlink was followed, log stat err=%v", err)
	}
}

func TestPrepareWasmShimLogRejectsFileSymlink(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shim-log")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, wasmShimLogFile)); err != nil {
		t.Fatal(err)
	}
	if err := prepareWasmShimLog(dir); err == nil {
		t.Fatal("expected symlink log file to be rejected")
	}
}

func TestUseWasmShimLogDirOpensRelativeLog(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shim-log")
	t.Chdir(t.TempDir())
	if err := useWasmShimLogDir(dir); err != nil {
		t.Fatalf("use: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("cwd %s, want %s", got, want)
	}
	f, err := os.Open(wasmShimLogFile)
	if err != nil {
		t.Fatalf("open relative log: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
