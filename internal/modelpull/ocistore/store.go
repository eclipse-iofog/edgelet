// Package ocistore is the shared on-disk store for OCI model artifacts.
package ocistore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	layoutVersion = "1.0.0"
	layoutFile    = "layout.json"
	indexFile     = "models.json"
	refsFile      = "refs.json"
	blobsDir      = "blobs"
	manifestsDir  = "manifests"
	incompleteExt = ".incomplete"
	staleMaxAge   = 7 * 24 * time.Hour
)

// Layout is the DMR-compatible store marker.
type Layout struct {
	Version string `json:"version"`
}

// Index is the DMR-compatible models.json document.
type Index struct {
	Models []IndexEntry `json:"models"`
}

// IndexEntry records one stored artifact and its tags.
type IndexEntry struct {
	ID    string   `json:"id"`
	Tags  []string `json:"tags"`
	Files []string `json:"files"`
}

// ModelRef records which blobs and manifest a local model name claims.
type ModelRef struct {
	Digest string   `json:"digest,omitempty"`
	Blobs  []string `json:"blobs"`
}

type refIndex struct {
	Models map[string]ModelRef `json:"models"`
}

// Store is a local content-addressed model artifact store.
type Store struct {
	root string
	mu   sync.Mutex
}

// Open initializes (or opens) a store at root.
func Open(root string) (*Store, error) {
	s := &Store{root: root}
	if err := os.MkdirAll(root, 0o755); err != nil { // #nosec G301 -- oci-store root must be traversable for inspect
		return nil, fmt.Errorf("create model store: %w", err)
	}
	if err := s.ensureLayout(); err != nil {
		return nil, err
	}
	if _, err := os.Stat(s.indexPath()); errors.Is(err, os.ErrNotExist) {
		if err := s.writeIndex(Index{Models: []IndexEntry{}}); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("stat models index: %w", err)
	}
	return s, nil
}

// Root returns the store directory.
func (s *Store) Root() string {
	return s.root
}

func (s *Store) layoutPath() string { return filepath.Join(s.root, layoutFile) }
func (s *Store) indexPath() string  { return filepath.Join(s.root, indexFile) }
func (s *Store) blobsRoot() string  { return filepath.Join(s.root, blobsDir) }
func (s *Store) manifestsRoot() string {
	return filepath.Join(s.root, manifestsDir)
}

func (s *Store) ensureLayout() error {
	if _, err := os.Stat(s.layoutPath()); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat layout: %w", err)
	}
	return s.writeLayout(Layout{Version: layoutVersion})
}

func (s *Store) writeLayout(layout Layout) error {
	raw, err := json.MarshalIndent(layout, "", " ")
	if err != nil {
		return fmt.Errorf("encode layout: %w", err)
	}
	raw = append(raw, '\n')
	return writeFileAtomic(s.layoutPath(), raw)
}

// ReadLayout returns the store layout document.
func (s *Store) ReadLayout() (Layout, error) {
	raw, err := os.ReadFile(s.layoutPath())
	if err != nil {
		return Layout{}, fmt.Errorf("read layout: %w", err)
	}
	var layout Layout
	if err := json.Unmarshal(raw, &layout); err != nil {
		return Layout{}, fmt.Errorf("decode layout: %w", err)
	}
	return layout, nil
}

// ReadIndex returns the models.json index.
func (s *Store) ReadIndex() (Index, error) {
	raw, err := os.ReadFile(s.indexPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Index{Models: []IndexEntry{}}, nil
		}
		return Index{}, fmt.Errorf("read models index: %w", err)
	}
	var idx Index
	if err := json.Unmarshal(raw, &idx); err != nil {
		return Index{}, fmt.Errorf("decode models index: %w", err)
	}
	if idx.Models == nil {
		idx.Models = []IndexEntry{}
	}
	return idx, nil
}

func (s *Store) writeIndex(idx Index) error {
	if idx.Models == nil {
		idx.Models = []IndexEntry{}
	}
	raw, err := json.MarshalIndent(idx, "", " ")
	if err != nil {
		return fmt.Errorf("encode models index: %w", err)
	}
	raw = append(raw, '\n')
	return writeFileAtomic(s.indexPath(), raw)
}

func (s *Store) refsPath() string { return filepath.Join(s.root, refsFile) }

func (s *Store) readRefs() (refIndex, error) {
	raw, err := os.ReadFile(s.refsPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return refIndex{Models: map[string]ModelRef{}}, nil
		}
		return refIndex{}, fmt.Errorf("read blob refs: %w", err)
	}
	var refs refIndex
	if err := json.Unmarshal(raw, &refs); err != nil {
		return refIndex{}, fmt.Errorf("decode blob refs: %w", err)
	}
	if refs.Models == nil {
		refs.Models = map[string]ModelRef{}
	}
	return refs, nil
}

func (s *Store) writeRefs(refs refIndex) error {
	if refs.Models == nil {
		refs.Models = map[string]ModelRef{}
	}
	raw, err := json.MarshalIndent(refs, "", " ")
	if err != nil {
		return fmt.Errorf("encode blob refs: %w", err)
	}
	raw = append(raw, '\n')
	return writeFileAtomic(s.refsPath(), raw)
}

// Track records that modelName claims digest and blobs. Replaces any previous claim.
func (s *Store) Track(modelName, digest string, blobs []string) error {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return errors.New("model name is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	refs, err := s.readRefs()
	if err != nil {
		return err
	}
	cleaned := uniqueDigests(blobs)
	refs.Models[modelName] = ModelRef{
		Digest: strings.TrimSpace(digest),
		Blobs:  cleaned,
	}
	return s.writeRefs(refs)
}

// Release drops modelName's blob claims. Blobs still claimed by another model are kept.
func (s *Store) Release(modelName string) error {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	refs, err := s.readRefs()
	if err != nil {
		return err
	}
	if _, ok := refs.Models[modelName]; !ok {
		return nil
	}
	delete(refs.Models, modelName)
	return s.writeRefs(refs)
}

// CollectUnused deletes blobs and manifests that no remaining model claims.
func (s *Store) CollectUnused() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	refs, err := s.readRefs()
	if err != nil {
		return 0, err
	}
	usedBlobs := map[string]struct{}{}
	usedManifests := map[string]struct{}{}
	for _, rec := range refs.Models {
		if d := strings.TrimSpace(rec.Digest); d != "" {
			usedManifests[d] = struct{}{}
		}
		for _, blob := range rec.Blobs {
			if blob = strings.TrimSpace(blob); blob != "" {
				usedBlobs[blob] = struct{}{}
			}
		}
	}

	removed := 0
	blobRoot := s.blobsRoot()
	if _, err := os.Stat(blobRoot); err == nil {
		walkErr := filepath.WalkDir(blobRoot, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				if errors.Is(walkErr, os.ErrNotExist) {
					return nil
				}
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			name := d.Name()
			if strings.HasSuffix(name, incompleteExt) {
				return nil
			}
			rel, err := filepath.Rel(blobRoot, path)
			if err != nil {
				return err
			}
			algo, hexPart := splitBlobRel(rel)
			if algo == "" || hexPart == "" {
				return nil
			}
			digest := algo + ":" + hexPart
			if _, ok := usedBlobs[digest]; ok {
				return nil
			}
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) { // #nosec G122 -- path from WalkDir under the oci-store blob root
				return fmt.Errorf("remove unreferenced blob: %w", err)
			}
			removed++
			return nil
		})
		if walkErr != nil {
			return removed, walkErr
		}
	}

	idx, err := s.ReadIndex()
	if err != nil {
		return removed, err
	}
	kept := make([]IndexEntry, 0, len(idx.Models))
	for _, entry := range idx.Models {
		if _, ok := usedManifests[entry.ID]; ok {
			kept = append(kept, entry)
			continue
		}
		if path, pathErr := s.manifestPath(entry.ID); pathErr == nil {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return removed, fmt.Errorf("remove unreferenced manifest: %w", err)
			}
		}
	}
	if len(kept) != len(idx.Models) {
		idx.Models = kept
		if err := s.writeIndex(idx); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

// CleanupStaleIncomplete removes partial blob downloads older than seven days.
func (s *Store) CleanupStaleIncomplete(now time.Time) (int, error) {
	if now.IsZero() {
		now = time.Now()
	}
	cutoff := now.Add(-staleMaxAge)
	removed := 0
	root := s.blobsRoot()
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), incompleteExt) {
			return nil
		}
		st, err := os.Stat(path)
		if err != nil {
			return nil
		}
		if st.ModTime().After(cutoff) {
			return nil
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) { // #nosec G122 -- path from WalkDir under the oci-store blob root
			return err
		}
		removed++
		return nil
	})
	return removed, err
}

func uniqueDigests(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, d := range in {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	return out
}

func splitBlobRel(rel string) (algo, hexPart string) {
	rel = filepath.ToSlash(rel)
	parts := strings.Split(rel, "/")
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

func parseDigest(digest string) (algo, hexPart string, err error) {
	digest = strings.TrimSpace(digest)
	algo, hexPart, ok := strings.Cut(digest, ":")
	if !ok || algo == "" || hexPart == "" {
		return "", "", fmt.Errorf("invalid digest %q", digest)
	}
	wantLen := 0
	switch algo {
	case "sha256":
		wantLen = 64
	case "sha512":
		wantLen = 128
	default:
		return "", "", fmt.Errorf("unsupported digest algorithm %q", algo)
	}
	if len(hexPart) != wantLen {
		return "", "", fmt.Errorf("invalid digest %q", digest)
	}
	for _, c := range hexPart {
		if !unicode.Is(unicode.ASCII_Hex_Digit, c) {
			return "", "", fmt.Errorf("invalid digest %q", digest)
		}
	}
	return algo, strings.ToLower(hexPart), nil
}

func (s *Store) blobPath(digest string) (string, error) {
	algo, hexPart, err := parseDigest(digest)
	if err != nil {
		return "", err
	}
	path := filepath.Join(s.root, blobsDir, algo, hexPart)
	return s.safeUnderRoot(path)
}

func (s *Store) manifestPath(digest string) (string, error) {
	algo, hexPart, err := parseDigest(digest)
	if err != nil {
		return "", err
	}
	path := filepath.Join(s.root, manifestsDir, algo, hexPart)
	return s.safeUnderRoot(path)
}

func (s *Store) safeUnderRoot(path string) (string, error) {
	cleanRoot := filepath.Clean(s.root)
	cleanPath := filepath.Clean(path)
	rel, err := filepath.Rel(cleanRoot, cleanPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path %q escapes model store", path)
	}
	return cleanPath, nil
}

// HasBlob reports whether a complete blob is present.
func (s *Store) HasBlob(digest string) (bool, error) {
	path, err := s.blobPath(digest)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("stat blob: %w", err)
}

// IncompleteSize returns the size of a partial blob download, or 0.
func (s *Store) IncompleteSize(digest string) (int64, error) {
	path, err := s.blobPath(digest)
	if err != nil {
		return 0, err
	}
	st, err := os.Stat(path + incompleteExt)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("stat incomplete blob: %w", err)
	}
	return st.Size(), nil
}

// RemoveIncomplete deletes a partial blob download.
func (s *Store) RemoveIncomplete(digest string) error {
	path, err := s.blobPath(digest)
	if err != nil {
		return err
	}
	if err := os.Remove(path + incompleteExt); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove incomplete blob: %w", err)
	}
	return nil
}

// WriteBlob writes a blob from offset 0 (or replaces an incomplete file).
// expectedSize, when > 0, is the full blob size used to detect a partial download.
func (s *Store) WriteBlob(digest string, r io.Reader, expectedSize int64) error {
	return s.writeBlob(digest, r, false, expectedSize)
}

// AppendBlob appends to an existing incomplete download.
func (s *Store) AppendBlob(digest string, r io.Reader, expectedSize int64) error {
	return s.writeBlob(digest, r, true, expectedSize)
}

func (s *Store) writeBlob(digest string, r io.Reader, appendIncomplete bool, expectedSize int64) error {
	has, err := s.HasBlob(digest)
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	path, err := s.blobPath(digest)
	if err != nil {
		return err
	}
	incomplete := path + incompleteExt
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { // #nosec G301 -- digest-addressed blob dirs must be traversable
		return fmt.Errorf("create blob directory: %w", err)
	}

	if !appendIncomplete {
		if err := os.Remove(incomplete); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("reset incomplete blob: %w", err)
		}
	}

	flag := os.O_CREATE | os.O_WRONLY
	if appendIncomplete {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
	}
	f, err := os.OpenFile(incomplete, flag, 0o644) // #nosec G304,G302 -- path is digest-addressed under the store root; blobs are world-readable
	if err != nil {
		return fmt.Errorf("open incomplete blob: %w", err)
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return fmt.Errorf("write blob %s: %w", digest, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync blob %s: %w", digest, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close blob %s: %w", digest, err)
	}

	st, err := os.Stat(incomplete)
	if err != nil {
		return fmt.Errorf("stat incomplete blob: %w", err)
	}
	if expectedSize > 0 && st.Size() < expectedSize {
		return fmt.Errorf("incomplete download of blob %s: have %d of %d bytes", digest, st.Size(), expectedSize)
	}
	if err := verifyBlobFile(incomplete, digest); err != nil {
		return err
	}
	if err := os.Rename(incomplete, path); err != nil {
		return fmt.Errorf("commit blob %s: %w", digest, err)
	}
	return nil
}

func verifyBlobFile(path, digest string) error {
	algo, hexPart, err := parseDigest(digest)
	if err != nil {
		return err
	}
	if algo != "sha256" {
		return fmt.Errorf("cannot verify digest algorithm %q", algo)
	}
	f, err := os.Open(path) // #nosec G304 -- path is digest-addressed and checked by safeUnderRoot
	if err != nil {
		return fmt.Errorf("open blob for verify: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash blob: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != hexPart {
		_ = os.Remove(path)
		return fmt.Errorf("blob digest mismatch: expected sha256:%s, got sha256:%s", hexPart, got)
	}
	return nil
}

// Open opens a complete blob for reading.
func (s *Store) Open(digest string) (io.ReadCloser, error) {
	path, err := s.blobPath(digest)
	if err != nil {
		return nil, err
	}
	return os.Open(path) // #nosec G304 -- path is digest-addressed and checked by safeUnderRoot
}

// ReadBlob returns the complete blob bytes.
func (s *Store) ReadBlob(digest string) ([]byte, error) {
	path, err := s.blobPath(digest)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path) // #nosec G304 -- path is digest-addressed and checked by safeUnderRoot
}

// WriteManifest stores the raw OCI manifest and updates models.json.
func (s *Store) WriteManifest(digest string, raw []byte, tags []string, files []string) error {
	path, err := s.manifestPath(digest)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(path, raw); err != nil {
		return fmt.Errorf("write manifest %s: %w", digest, err)
	}
	idx, err := s.ReadIndex()
	if err != nil {
		return err
	}
	idx = idx.upsert(digest, tags, files)
	return s.writeIndex(idx)
}

func (i Index) upsert(id string, tags, files []string) Index {
	found := false
	for n, entry := range i.Models {
		if entry.ID == id {
			i.Models[n].Tags = mergeTags(entry.Tags, tags)
			if len(files) > 0 {
				i.Models[n].Files = files
			}
			found = true
			continue
		}
		i.Models[n].Tags = subtractTags(entry.Tags, tags)
	}
	if !found {
		i.Models = append(i.Models, IndexEntry{
			ID:    id,
			Tags:  append([]string{}, tags...),
			Files: append([]string{}, files...),
		})
	}
	return i
}

func mergeTags(existing, add []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(existing)+len(add))
	for _, t := range existing {
		if _, ok := seen[t]; ok || t == "" {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	for _, t := range add {
		if _, ok := seen[t]; ok || t == "" {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

func subtractTags(existing, remove []string) []string {
	drop := map[string]struct{}{}
	for _, t := range remove {
		drop[t] = struct{}{}
	}
	out := make([]string, 0, len(existing))
	for _, t := range existing {
		if _, ok := drop[t]; ok {
			continue
		}
		out = append(out, t)
	}
	return out
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil { // #nosec G301 -- store metadata dirs must be traversable
		return fmt.Errorf("create directory %q: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil { // #nosec G302 -- store index and manifests are world-readable on the node
		cleanup()
		return fmt.Errorf("chmod temporary file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("rename temporary file: %w", err)
	}
	return nil
}
