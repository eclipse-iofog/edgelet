package ocistore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sha256Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestOpen_WritesDMRLayout(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	layoutRaw, err := os.ReadFile(filepath.Join(root, "layout.json"))
	if err != nil {
		t.Fatalf("read layout.json: %v", err)
	}
	var layout Layout
	if err := json.Unmarshal(layoutRaw, &layout); err != nil {
		t.Fatalf("decode layout.json: %v", err)
	}
	if layout.Version != "1.0.0" {
		t.Fatalf("layout version: got %q", layout.Version)
	}

	indexRaw, err := os.ReadFile(filepath.Join(root, "models.json"))
	if err != nil {
		t.Fatalf("read models.json: %v", err)
	}
	var idx Index
	if err := json.Unmarshal(indexRaw, &idx); err != nil {
		t.Fatalf("decode models.json: %v", err)
	}
	if idx.Models == nil {
		t.Fatal("models.json models must be an array")
	}

	gotLayout, err := store.ReadLayout()
	if err != nil || gotLayout.Version != "1.0.0" {
		t.Fatalf("ReadLayout: %+v err=%v", gotLayout, err)
	}
}

func TestWriteBlobAndManifest_IndexStructure(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	blob := []byte("model-weights")
	blobDigest := sha256Digest(blob)
	if err := store.WriteBlob(blobDigest, bytes.NewReader(blob), int64(len(blob))); err != nil {
		t.Fatalf("write blob: %v", err)
	}
	manifest := []byte(`{"schemaVersion":2}`)
	manifestDigest := sha256Digest(manifest)
	if err := store.WriteManifest(manifestDigest, manifest, []string{"ai/gemma3:latest"}, []string{blobDigest}); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	idx, err := store.ReadIndex()
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if len(idx.Models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(idx.Models))
	}
	entry := idx.Models[0]
	if entry.ID != manifestDigest {
		t.Fatalf("id: got %q", entry.ID)
	}
	if len(entry.Tags) != 1 || entry.Tags[0] != "ai/gemma3:latest" {
		t.Fatalf("tags: %v", entry.Tags)
	}
	if len(entry.Files) != 1 || entry.Files[0] != blobDigest {
		t.Fatalf("files: %v", entry.Files)
	}

	algo, hexPart, _ := parseDigest(blobDigest)
	if _, err := os.Stat(filepath.Join(store.Root(), "blobs", algo, hexPart)); err != nil {
		t.Fatalf("blob path missing: %v", err)
	}
	_, hexManifest, _ := parseDigest(manifestDigest)
	if _, err := os.Stat(filepath.Join(store.Root(), "manifests", "sha256", hexManifest)); err != nil {
		t.Fatalf("manifest path missing: %v", err)
	}

	if err := store.WriteManifest(manifestDigest, manifest, []string{"ai/gemma3:4b-q8_0"}, nil); err != nil {
		t.Fatalf("add tag: %v", err)
	}
	idx, err = store.ReadIndex()
	if err != nil {
		t.Fatalf("re-read index: %v", err)
	}
	if len(idx.Models) != 1 || len(idx.Models[0].Tags) != 2 {
		t.Fatalf("expected two tags on one entry, got %+v", idx.Models)
	}
}

func TestWriteBlob_ResumeIncomplete(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	full := bytes.Repeat([]byte("a"), 1024)
	digest := sha256Digest(full)

	err = store.WriteBlob(digest, bytes.NewReader(full[:200]), int64(len(full)))
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("expected incomplete write, got %v", err)
	}
	n, err := store.IncompleteSize(digest)
	if err != nil || n != 200 {
		t.Fatalf("incomplete size: %d err=%v", n, err)
	}

	if err := store.AppendBlob(digest, bytes.NewReader(full[200:]), int64(len(full))); err != nil {
		t.Fatalf("append: %v", err)
	}
	has, err := store.HasBlob(digest)
	if err != nil || !has {
		t.Fatalf("expected complete blob, has=%v err=%v", has, err)
	}
	got, err := store.ReadBlob(digest)
	if err != nil || !bytes.Equal(got, full) {
		t.Fatal("blob contents mismatch")
	}
}

func TestBlobRefs_SharedDigestRetainedUntilLastRelease(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	blob := []byte("shared-weights")
	blobDigest := sha256Digest(blob)
	if err := store.WriteBlob(blobDigest, bytes.NewReader(blob), int64(len(blob))); err != nil {
		t.Fatalf("write blob: %v", err)
	}
	manifest := []byte(`{"schemaVersion":2}`)
	manifestDigest := sha256Digest(manifest)
	if err := store.WriteManifest(manifestDigest, manifest, []string{"ai/shared:latest"}, []string{blobDigest}); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := store.Track("model-a", manifestDigest, []string{blobDigest}); err != nil {
		t.Fatalf("track a: %v", err)
	}
	if err := store.Track("model-b", manifestDigest, []string{blobDigest}); err != nil {
		t.Fatalf("track b: %v", err)
	}

	if err := store.Release("model-a"); err != nil {
		t.Fatalf("release a: %v", err)
	}
	removed, err := store.CollectUnused()
	if err != nil {
		t.Fatalf("collect after first release: %v", err)
	}
	if removed != 0 {
		t.Fatalf("shared blob must be retained, removed=%d", removed)
	}
	has, err := store.HasBlob(blobDigest)
	if err != nil || !has {
		t.Fatalf("expected shared blob retained, has=%v err=%v", has, err)
	}

	if err := store.Release("model-b"); err != nil {
		t.Fatalf("release b: %v", err)
	}
	removed, err = store.CollectUnused()
	if err != nil {
		t.Fatalf("collect after last release: %v", err)
	}
	if removed == 0 {
		t.Fatal("expected unreferenced blob deleted")
	}
	has, err = store.HasBlob(blobDigest)
	if err != nil || has {
		t.Fatalf("expected blob removed, has=%v err=%v", has, err)
	}
	idx, err := store.ReadIndex()
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if len(idx.Models) != 0 {
		t.Fatalf("expected unused manifest dropped, got %+v", idx.Models)
	}
}

func TestCleanupStaleIncomplete_RemovesOldPartials(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	digest := "sha256:" + strings.Repeat("ab", 32)
	path, err := store.blobPath(digest)
	if err != nil {
		t.Fatalf("blob path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	incomplete := path + incompleteExt
	if err := os.WriteFile(incomplete, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(incomplete, old, old); err != nil {
		t.Fatal(err)
	}
	removed, err := store.CleanupStaleIncomplete(time.Now())
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if removed != 1 {
		t.Fatalf("expected 1 stale file, got %d", removed)
	}
	if _, err := os.Stat(incomplete); !os.IsNotExist(err) {
		t.Fatal("expected stale incomplete blob removed")
	}
}

func TestWriteBlob_RejectsMismatch(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	payload := []byte("hello")
	wrong := "sha256:" + strings.Repeat("0", 64)
	err = store.WriteBlob(wrong, bytes.NewReader(payload), int64(len(payload)))
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("expected digest mismatch, got %v", err)
	}
}
