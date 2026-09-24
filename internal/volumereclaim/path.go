package volumereclaim

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func (r *Reclaimer) dataRoot() string {
	return filepath.Join(r.diskDirectory, "volumes", "data")
}

func (r *Reclaimer) sharedRoot() string {
	return filepath.Join(r.diskDirectory, "volumes", "shared")
}

func jailedUnder(path, root string) (string, error) {
	path = strings.TrimSpace(path)
	root = strings.TrimSpace(root)
	if path == "" || root == "" {
		return "", ErrPathJail
	}
	absPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrPathJail, err)
	}
	absRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrPathJail, err)
	}
	if resolved, evalErr := filepath.EvalSymlinks(absRoot); evalErr == nil {
		absRoot = resolved
	}
	if resolved, evalErr := filepath.EvalSymlinks(absPath); evalErr == nil {
		absPath = resolved
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%w: %s", ErrPathJail, path)
	}
	return absPath, nil
}

func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total
}

func (r *Reclaimer) destroyJailed(path, root string) (int64, error) {
	jailed, err := jailedUnder(path, root)
	if err != nil {
		return 0, err
	}
	bytes := dirSize(jailed)
	if err := os.RemoveAll(jailed); err != nil {
		return bytes, err
	}
	return bytes, nil
}
