//go:build linux && !cgo

package data

import (
	"os"
	"path/filepath"
	"testing"
)

const (
	testCurrentHash  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testPreviousHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testOrphanHash   = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	testOrphanHash2  = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	testExecHash     = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
)

func setupPruneRoot(t *testing.T) (root, dataRoot string) {
	t.Helper()
	root = t.TempDir()
	dataRoot = filepath.Join(root, "data")
	if err := os.MkdirAll(dataRoot, 0755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	return root, dataRoot
}

func symlinkBundle(t *testing.T, dataRoot, linkName, hash string) {
	t.Helper()
	dir := filepath.Join(dataRoot, hash)
	if err := os.Symlink(dir, filepath.Join(dataRoot, linkName)); err != nil {
		t.Fatalf("symlink %s: %v", linkName, err)
	}
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be gone, stat err=%v", path, err)
	}
}

func TestPruneStaleExtractsRemovesOrphansKeepsCurrentPrevious(t *testing.T) {
	t.Parallel()

	root, dataRoot := setupPruneRoot(t)
	writeReadyBundleDir(t, filepath.Join(dataRoot, testCurrentHash))
	writeReadyBundleDir(t, filepath.Join(dataRoot, testPreviousHash))
	writeReadyBundleDir(t, filepath.Join(dataRoot, testOrphanHash))
	writeReadyBundleDir(t, filepath.Join(dataRoot, testOrphanHash2))
	if err := os.MkdirAll(filepath.Join(dataRoot, "cni"), 0755); err != nil {
		t.Fatalf("mkdir cni: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataRoot, ".lock"), []byte{}, 0600); err != nil {
		t.Fatalf("write .lock: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataRoot, "notes.txt"), []byte("keep"), 0644); err != nil {
		t.Fatalf("write notes: %v", err)
	}
	orphanTmp := filepath.Join(dataRoot, testOrphanHash+"-tmp")
	if err := os.MkdirAll(orphanTmp, 0755); err != nil {
		t.Fatalf("mkdir orphan-tmp: %v", err)
	}
	currentTmp := filepath.Join(dataRoot, testCurrentHash+"-tmp")
	if err := os.MkdirAll(currentTmp, 0755); err != nil {
		t.Fatalf("mkdir current-tmp: %v", err)
	}
	symlinkBundle(t, dataRoot, "current", testCurrentHash)
	symlinkBundle(t, dataRoot, "previous", testPreviousHash)

	if err := pruneStaleExtracts(root, testCurrentHash, ""); err != nil {
		t.Fatalf("pruneStaleExtracts: %v", err)
	}

	mustExist(t, filepath.Join(dataRoot, testCurrentHash))
	mustExist(t, filepath.Join(dataRoot, testPreviousHash))
	mustExist(t, filepath.Join(dataRoot, "cni"))
	mustExist(t, filepath.Join(dataRoot, "notes.txt"))
	mustExist(t, currentTmp)
	mustNotExist(t, filepath.Join(dataRoot, testOrphanHash))
	mustNotExist(t, filepath.Join(dataRoot, testOrphanHash2))
	mustNotExist(t, orphanTmp)
	if _, err := os.Lstat(filepath.Join(dataRoot, "current")); err != nil {
		t.Fatalf("current symlink: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dataRoot, "previous")); err != nil {
		t.Fatalf("previous symlink: %v", err)
	}
}

func TestPruneStaleExtractsWithoutPrevious(t *testing.T) {
	t.Parallel()

	root, dataRoot := setupPruneRoot(t)
	writeReadyBundleDir(t, filepath.Join(dataRoot, testCurrentHash))
	writeReadyBundleDir(t, filepath.Join(dataRoot, testOrphanHash))
	symlinkBundle(t, dataRoot, "current", testCurrentHash)

	if err := pruneStaleExtracts(root, testCurrentHash, ""); err != nil {
		t.Fatalf("pruneStaleExtracts: %v", err)
	}

	mustExist(t, filepath.Join(dataRoot, testCurrentHash))
	mustNotExist(t, filepath.Join(dataRoot, testOrphanHash))
}

func TestPruneStaleExtractsKeepsIncompletePrevious(t *testing.T) {
	t.Parallel()

	root, dataRoot := setupPruneRoot(t)
	writeReadyBundleDir(t, filepath.Join(dataRoot, testCurrentHash))
	if err := os.MkdirAll(filepath.Join(dataRoot, testPreviousHash, "bin"), 0755); err != nil {
		t.Fatalf("mkdir incomplete previous: %v", err)
	}
	writeReadyBundleDir(t, filepath.Join(dataRoot, testOrphanHash))
	symlinkBundle(t, dataRoot, "current", testCurrentHash)
	symlinkBundle(t, dataRoot, "previous", testPreviousHash)

	if err := pruneStaleExtracts(root, testCurrentHash, ""); err != nil {
		t.Fatalf("pruneStaleExtracts: %v", err)
	}

	mustExist(t, filepath.Join(dataRoot, testPreviousHash))
	mustNotExist(t, filepath.Join(dataRoot, testOrphanHash))
}

func TestPruneStaleExtractsSkipsWhenCurrentNotReady(t *testing.T) {
	t.Parallel()

	root, dataRoot := setupPruneRoot(t)
	if err := os.MkdirAll(filepath.Join(dataRoot, testCurrentHash, "bin"), 0755); err != nil {
		t.Fatalf("mkdir incomplete current: %v", err)
	}
	writeReadyBundleDir(t, filepath.Join(dataRoot, testOrphanHash))
	symlinkBundle(t, dataRoot, "current", testCurrentHash)

	if err := pruneStaleExtracts(root, testCurrentHash, ""); err != nil {
		t.Fatalf("pruneStaleExtracts: %v", err)
	}

	mustExist(t, filepath.Join(dataRoot, testOrphanHash))
}

func TestPruneStaleExtractsSkipsWhenCurrentMismatchesEmbed(t *testing.T) {
	t.Parallel()

	root, dataRoot := setupPruneRoot(t)
	writeReadyBundleDir(t, filepath.Join(dataRoot, testCurrentHash))
	writeReadyBundleDir(t, filepath.Join(dataRoot, testOrphanHash))
	symlinkBundle(t, dataRoot, "current", testCurrentHash)

	if err := pruneStaleExtracts(root, testPreviousHash, ""); err != nil {
		t.Fatalf("pruneStaleExtracts: %v", err)
	}

	mustExist(t, filepath.Join(dataRoot, testOrphanHash))
}

func TestPruneStaleExtractsSkipsWhenCurrentMissing(t *testing.T) {
	t.Parallel()

	root, dataRoot := setupPruneRoot(t)
	writeReadyBundleDir(t, filepath.Join(dataRoot, testOrphanHash))

	if err := pruneStaleExtracts(root, testCurrentHash, ""); err != nil {
		t.Fatalf("pruneStaleExtracts: %v", err)
	}

	mustExist(t, filepath.Join(dataRoot, testOrphanHash))
}

func TestPruneStaleExtractsSkipsWhenNoEmbedHash(t *testing.T) {
	t.Parallel()

	root, dataRoot := setupPruneRoot(t)
	writeReadyBundleDir(t, filepath.Join(dataRoot, testCurrentHash))
	writeReadyBundleDir(t, filepath.Join(dataRoot, testOrphanHash))
	symlinkBundle(t, dataRoot, "current", testCurrentHash)

	if err := pruneStaleExtracts(root, "", ""); err != nil {
		t.Fatalf("pruneStaleExtracts: %v", err)
	}

	mustExist(t, filepath.Join(dataRoot, testOrphanHash))
}

func TestPruneStaleExtractsKeepsExecutableHash(t *testing.T) {
	t.Parallel()

	root, dataRoot := setupPruneRoot(t)
	writeReadyBundleDir(t, filepath.Join(dataRoot, testCurrentHash))
	writeReadyBundleDir(t, filepath.Join(dataRoot, testPreviousHash))
	writeReadyBundleDir(t, filepath.Join(dataRoot, testExecHash))
	writeReadyBundleDir(t, filepath.Join(dataRoot, testOrphanHash))
	symlinkBundle(t, dataRoot, "current", testCurrentHash)
	symlinkBundle(t, dataRoot, "previous", testPreviousHash)

	execPath := filepath.Join(dataRoot, testExecHash, "bin", "edgelet")
	if err := pruneStaleExtracts(root, testCurrentHash, execPath); err != nil {
		t.Fatalf("pruneStaleExtracts: %v", err)
	}

	mustExist(t, filepath.Join(dataRoot, testExecHash))
	mustNotExist(t, filepath.Join(dataRoot, testOrphanHash))
}

func TestIsBundleHashName(t *testing.T) {
	t.Parallel()

	if !isBundleHashName(testCurrentHash) {
		t.Fatal("expected 64-hex name to match")
	}
	if isBundleHashName("oldhash") {
		t.Fatal("short name must not match")
	}
	if isBundleHashName(testCurrentHash[:63] + "z") {
		t.Fatal("non-hex must not match")
	}
}

func TestBundleHashFromPath(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	execPath := filepath.Join(root, testCurrentHash, "bin", "edgelet")
	if got := bundleHashFromPath(root, execPath); got != testCurrentHash {
		t.Fatalf("bundleHashFromPath=%q want %q", got, testCurrentHash)
	}
	if got := bundleHashFromPath(root, "/usr/local/bin/edgelet"); got != "" {
		t.Fatalf("thin path should not yield hash, got %q", got)
	}
}
