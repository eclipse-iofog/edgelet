package modelpull

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CleanupStaleIncomplete removes leftover partial downloads older than maxAge.
func CleanupStaleIncomplete(root string, maxAge time.Duration, now time.Time) (int, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return 0, nil
	}
	if maxAge <= 0 {
		maxAge = StaleIncompleteAge
	}
	if now.IsZero() {
		now = time.Now()
	}
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if !info.IsDir() {
		return 0, nil
	}

	cutoff := now.Add(-maxAge)
	removed := 0
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		name := d.Name()
		if d.IsDir() {
			if strings.HasSuffix(name, ContentStagingSuffix) && path != root {
				if staleFile(path, cutoff) {
					if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) { // #nosec G122 -- path from WalkDir under the model disk root
						return err
					}
					removed++
					return fs.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(name, IncompleteExt) && !isTempWriteName(name) {
			return nil
		}
		if !staleFile(path, cutoff) {
			return nil
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) { // #nosec G122 -- path from WalkDir under the model disk root
			return err
		}
		removed++
		return nil
	})
	return removed, walkErr
}

func staleFile(path string, cutoff time.Time) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !st.ModTime().After(cutoff)
}

func isTempWriteName(name string) bool {
	return strings.Contains(name, ".tmp-")
}
