//go:build linux && !cgo

package data

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"github.com/eclipse-iofog/edgelet/pkg/datadir"
	"github.com/eclipse-iofog/edgelet/pkg/flock"
)

const bundleHashNameLen = 64

// pruneStaleBundles removes extracted embed trees that are not the live current
// or previous bundle. Best-effort: failures are logged and never fail extract.
func pruneStaleBundles(dataDir string) {
	if err := pruneStaleExtracts(dataDir, EmbeddedBundleHash(), liveExecutablePath()); err != nil {
		dataLogger.Warnf("Prune stale embed extracts (non-fatal): %v", err)
	}
}

func liveExecutablePath() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

func pruneStaleExtracts(dataDir, wantHash, execPath string) error {
	if wantHash == "" {
		dataLogger.Debugf("Skipping stale embed extract prune: no embedded bundle hash")
		return nil
	}

	root, err := datadir.BundleRoot(dataDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}

	lock, err := flock.Acquire(filepath.Join(root, ".lock"))
	if err != nil {
		return err
	}
	defer func() { _ = flock.Release(lock) }()

	currentHash, ok := resolvedBundleHash(root, "current")
	if !ok {
		dataLogger.Infof("Skipping stale embed extract prune: current bundle symlink missing or not a hash tree")
		return nil
	}
	currentDir := filepath.Join(root, currentHash)
	if reason := bundleReadyReason(currentDir); reason != "" {
		dataLogger.Infof("Skipping stale embed extract prune: current bundle not ready (%s)", reason)
		return nil
	}
	if currentHash != wantHash {
		dataLogger.Infof("Skipping stale embed extract prune: current=%s embedded=%s", currentHash, wantHash)
		return nil
	}

	keep := map[string]struct{}{
		currentHash: {},
		wantHash:    {},
	}
	if previousHash, ok := resolvedBundleHash(root, "previous"); ok {
		keep[previousHash] = struct{}{}
	}
	if execHash := bundleHashFromPath(root, execPath); execHash != "" {
		keep[execHash] = struct{}{}
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}

	var removed []string
	for _, ent := range entries {
		name := ent.Name()
		switch name {
		case "current", "previous", "cni", ".lock":
			continue
		}

		hash := staleExtractName(name)
		if hash == "" {
			continue
		}
		if _, ok := keep[hash]; ok {
			continue
		}

		target := filepath.Join(root, name)
		if err := os.RemoveAll(target); err != nil {
			dataLogger.Warnf("Remove stale embed extract %s (non-fatal): %v", name, err)
			continue
		}
		removed = append(removed, name)
	}

	if len(removed) > 0 {
		dataLogger.Infof("Removed stale embed extracts: %s", strings.Join(removed, ", "))
	}
	return nil
}

func resolvedBundleHash(root, linkName string) (string, bool) {
	target, err := os.Readlink(filepath.Join(root, linkName))
	if err != nil {
		return "", false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	base := filepath.Base(filepath.Clean(target))
	if !isBundleHashName(base) {
		return "", false
	}
	return base, true
}

func bundleHashFromPath(root, path string) string {
	if path == "" {
		return ""
	}
	cleaned := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		cleaned = resolved
	}
	rel, err := filepath.Rel(root, cleaned)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	base, _, _ := strings.Cut(rel, string(filepath.Separator))
	if !isBundleHashName(base) {
		return ""
	}
	return base
}

func staleExtractName(name string) string {
	if strings.HasSuffix(name, "-tmp") {
		hash := strings.TrimSuffix(name, "-tmp")
		if isBundleHashName(hash) {
			return hash
		}
		return ""
	}
	if isBundleHashName(name) {
		return name
	}
	return ""
}

func isBundleHashName(name string) bool {
	if len(name) != bundleHashNameLen {
		return false
	}
	_, err := hex.DecodeString(name)
	return err == nil
}
