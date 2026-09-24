package modelpull

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupStaleIncomplete_RemovesOldPartials(t *testing.T) {
	root := t.TempDir()
	oldFile := filepath.Join(root, "blob"+IncompleteExt)
	if err := os.WriteFile(oldFile, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(oldFile, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	fresh := filepath.Join(root, "fresh"+IncompleteExt)
	if err := os.WriteFile(fresh, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	staging := filepath.Join(root, ContentDirName+ContentStagingSuffix)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(staging, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	removed, err := CleanupStaleIncomplete(root, StaleIncompleteAge, time.Now())
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if removed < 2 {
		t.Fatalf("expected stale file and staging dir removed, got %d", removed)
	}
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Fatal("expected stale incomplete file removed")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh incomplete must remain: %v", err)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatal("expected stale staging directory removed")
	}
}
