package modelmanager

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/modelpull"
	"github.com/eclipse-iofog/edgelet/internal/modelpull/ocistore"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
)

const (
	PruneModeDangling = "dangling"
)

// PruneReport is the outcome of a model prune.
type PruneReport struct {
	Mode         string   `json:"mode"`
	Removed      []string `json:"removed"`
	BlobsRemoved int      `json:"blobsRemoved"`
	StaleCleaned int      `json:"staleCleaned"`
}

// PruneDangling removes unused local models (rows and on-disk trees) that are
// not in the keep set, then drops unreferenced blobs.
func (m *Manager) PruneDangling() (*PruneReport, error) {
	return m.Prune(PruneModeDangling)
}

// Prune removes unused model artifacts. The only supported mode is dangling.
func (m *Manager) Prune(mode string) (*PruneReport, error) {
	if m == nil || m.db == nil {
		return nil, errors.New("model manager is not initialized")
	}
	if m.db.Conn() == nil {
		return nil, errors.New("store is not initialized")
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = PruneModeDangling
	}
	if mode != PruneModeDangling {
		return nil, fmt.Errorf("invalid model prune mode %q (allowed: %s)", mode, PruneModeDangling)
	}

	stale, err := modelpull.CleanupStaleIncomplete(m.modelsRoot, modelpull.StaleIncompleteAge, m.now())
	if err != nil {
		return nil, err
	}
	if store, storeErr := m.openOCIStore(); storeErr == nil && store != nil {
		n, cleanErr := store.CleanupStaleIncomplete(m.now())
		if cleanErr != nil {
			return nil, cleanErr
		}
		stale += n
	}

	referenced, err := m.referencedNames()
	if err != nil {
		return nil, err
	}

	removedSet := make(map[string]struct{})
	entries, err := os.ReadDir(m.modelsRoot)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("list model directory: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == modelpull.OCIStoreDirName || strings.HasPrefix(name, ".") {
			continue
		}
		if _, kept := referenced[name]; kept {
			continue
		}
		if m.hasActivePull(name) {
			logging.LogInfo(moduleName, fmt.Sprintf("skip prune of %s: pull in progress", name))
			continue
		}
		if err := m.removeArtifacts(name); err != nil {
			return nil, err
		}
		removedSet[name] = struct{}{}
	}

	rows, err := m.db.ListLocalModels()
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row == nil {
			continue
		}
		name := strings.TrimSpace(row.Name)
		if name == "" {
			continue
		}
		if _, kept := referenced[name]; kept {
			continue
		}
		if m.hasActivePull(name) {
			logging.LogInfo(moduleName, fmt.Sprintf("skip prune of %s: pull in progress", name))
			continue
		}
		if err := m.removeArtifacts(name); err != nil {
			return nil, err
		}
		if err := m.db.DeleteModelRefs(name); err != nil {
			return nil, err
		}
		if err := m.db.DeleteLocalModel(name); err != nil {
			return nil, err
		}
		removedSet[name] = struct{}{}
	}

	removed := make([]string, 0, len(removedSet))
	for name := range removedSet {
		removed = append(removed, name)
	}
	slices.Sort(removed)

	blobsRemoved, err := m.collectUnusedBlobs()
	if err != nil {
		return nil, err
	}
	return &PruneReport{
		Mode:         mode,
		Removed:      removed,
		BlobsRemoved: blobsRemoved,
		StaleCleaned: stale,
	}, nil
}

// Remove deletes one deployed model, its on-disk tree, and unused shared blobs.
func (m *Manager) Remove(name string) error {
	if m == nil || m.db == nil {
		return errors.New("model manager is not initialized")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("model name is required")
	}
	if m.hasActivePull(name) {
		return fmt.Errorf("model %s is currently pulling", name)
	}
	refs, err := m.db.CountModelRefs(name)
	if err != nil {
		return err
	}
	if refs > 0 {
		return fmt.Errorf("model %s is bound to %d microservice(s)", name, refs)
	}
	if err := m.removeArtifacts(name); err != nil {
		return err
	}
	if err := m.db.DeleteModelRefs(name); err != nil {
		return err
	}
	if err := m.db.DeleteLocalModel(name); err != nil {
		return err
	}
	if _, err := m.collectUnusedBlobs(); err != nil {
		return err
	}
	return nil
}

func (m *Manager) referencedNames() (map[string]struct{}, error) {
	names, err := m.db.ListPruneKeepModelNames(!localModelsDisabled())
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(names))
	for _, name := range names {
		out[name] = struct{}{}
	}
	return out, nil
}

func (m *Manager) removeArtifacts(name string) error {
	if err := m.releaseOCI(name); err != nil {
		return err
	}
	dir := modelpull.ModelDir(m.modelsRoot, name)
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove model directory %s: %w", name, err)
	}
	staging := modelpull.ContentStagingDir(m.modelsRoot, name)
	if err := os.RemoveAll(staging); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove model staging directory %s: %w", name, err)
	}
	return nil
}

func (m *Manager) releaseOCI(name string) error {
	store, err := m.openOCIStore()
	if err != nil || store == nil {
		return err
	}
	onDisk, readErr := modelpull.ReadOnDiskManifest(modelpull.ManifestPath(m.modelsRoot, name))
	if readErr == nil && (onDisk.Digest != "" || len(onDisk.Blobs) > 0) {
		if err := store.Track(name, onDisk.Digest, onDisk.Blobs); err != nil {
			return err
		}
	}
	return store.Release(name)
}

func (m *Manager) collectUnusedBlobs() (int, error) {
	store, err := m.openOCIStore()
	if err != nil || store == nil {
		return 0, err
	}
	return store.CollectUnused()
}

func (m *Manager) openOCIStore() (*ocistore.Store, error) {
	root := modelpull.OCIStoreDir(m.modelsRoot)
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return ocistore.Open(root)
}
