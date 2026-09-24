package modelpull

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// OnDiskManifest is the per-model state file written to {metadata.name}/manifest.json.
type OnDiskManifest struct {
	MetadataName      string          `json:"metadataName"`
	RegistryID        int             `json:"registryId"`
	RegistryType      string          `json:"registryType"`
	Repo              string          `json:"repo"`
	RequestedRevision string          `json:"requestedRevision"`
	ResolvedRevision  string          `json:"resolvedRevision"`
	Digest            string          `json:"digest"`
	Format            string          `json:"format"`
	Files             []string        `json:"files"`
	ContentPaths      []string        `json:"contentPaths"`
	Blobs             []string        `json:"blobs,omitempty"`
	TotalBytes        int64           `json:"totalBytes"`
	RevisionFloating  bool            `json:"revisionFloating"`
	RevisionKind      string          `json:"revisionKind"`
	PulledAt          string          `json:"pulledAt"`
	OCIModelConfig    json.RawMessage `json:"ociModelConfig,omitempty"`
	IdentityKey       string          `json:"identityKey,omitempty"`
}

// ReadOnDiskManifest loads {metadata.name}/manifest.json when present.
func ReadOnDiskManifest(path string) (OnDiskManifest, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- path is {modelsRoot}/{name}/manifest.json from ModelDir helpers
	if err != nil {
		return OnDiskManifest{}, err
	}
	var m OnDiskManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return OnDiskManifest{}, fmt.Errorf("decode model manifest: %w", err)
	}
	if m.Files == nil {
		m.Files = []string{}
	}
	if m.ContentPaths == nil {
		m.ContentPaths = []string{}
	}
	return m, nil
}

// WriteOnDiskManifest writes manifest.json atomically next to the materialized content.
func WriteOnDiskManifest(path string, m OnDiskManifest) error {
	if m.Files == nil {
		m.Files = []string{}
	}
	if m.ContentPaths == nil {
		m.ContentPaths = []string{}
	}
	if m.PulledAt == "" {
		m.PulledAt = time.Now().UTC().Format(time.RFC3339)
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode model manifest: %w", err)
	}
	raw = append(raw, '\n')
	return writeFileAtomic(path, raw)
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil { // #nosec G301 -- per-model dir must be traversable for inspect and bind mounts
		return fmt.Errorf("create directory %q: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = os.Remove(tmpName)
	}
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
	if err := os.Chmod(tmpName, 0o644); err != nil { // #nosec G302 -- model metadata is world-readable on the node
		cleanup()
		return fmt.Errorf("chmod temporary file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("rename temporary file: %w", err)
	}
	return nil
}
