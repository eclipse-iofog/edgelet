package processmanager

import (
	"os"
	"path/filepath"
	"strings"
)

// PersistentVolumeTrees returns the host trees that exclusive-lock VOLUME
// workloads open: volumes/data (private) and volumes/shared.
func PersistentVolumeTrees(diskDirectory string) []string {
	diskDirectory = strings.TrimSpace(diskDirectory)
	if diskDirectory == "" {
		return nil
	}
	return []string{
		filepath.Join(diskDirectory, "volumes", "data"),
		filepath.Join(diskDirectory, "volumes", "shared"),
	}
}

// PathHoldsVolumeTree reports whether target is the tree root or a path under it.
func PathHoldsVolumeTree(target string, trees []string) bool {
	target = strings.TrimSpace(target)
	if target == "" || len(trees) == 0 {
		return false
	}
	if strings.HasPrefix(target, "pipe:") || strings.HasPrefix(target, "socket:") || strings.HasPrefix(target, "anon_inode:") {
		return false
	}
	cleaned := filepath.Clean(target)
	for _, tree := range trees {
		tree = strings.TrimSpace(tree)
		if tree == "" {
			continue
		}
		tree = filepath.Clean(tree)
		if cleaned == tree {
			return true
		}
		sep := string(os.PathSeparator)
		if strings.HasPrefix(cleaned, tree+sep) {
			return true
		}
	}
	return false
}

func expandVolumeTrees(diskDirectory string) []string {
	return expandHolderPaths(PersistentVolumeTrees(diskDirectory))
}

func expandHolderPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	expanded := make([]string, 0, len(paths)*2)
	seen := make(map[string]struct{}, len(paths)*2)
	add := func(path string) {
		path = filepath.Clean(strings.TrimSpace(path))
		if path == "" || path == "." {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		expanded = append(expanded, path)
	}
	for _, path := range paths {
		add(path)
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			add(resolved)
		}
	}
	return expanded
}
