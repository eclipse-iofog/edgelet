//go:build linux

package processmanager

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// ListVolumeTreeHolders returns host PIDs that still have an open file under
// volumes/data or volumes/shared. It does not delete volume files.
func ListVolumeTreeHolders(diskDirectory string) ([]int, error) {
	return listProcPathHolders(expandVolumeTrees(diskDirectory))
}

// ListVolumePathHolders returns host PIDs that have an open file on or under
// the given directories. It does not delete volume files.
func ListVolumePathHolders(paths []string) ([]int, error) {
	return listProcPathHolders(expandHolderPaths(paths))
}

func listProcPathHolders(targets []string) ([]int, error) {
	if len(targets) == 0 {
		return nil, nil
	}

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("scan volume holders: %w", err)
	}

	self := os.Getpid()
	holders := make([]int, 0)
	seen := make(map[int]struct{})
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil || pid <= 1 || pid == self {
			continue
		}
		if !processHoldsVolumeTrees(pid, targets) {
			continue
		}
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		holders = append(holders, pid)
	}
	return holders, nil
}

func processHoldsVolumeTrees(pid int, trees []string) bool {
	fdDir := fmt.Sprintf("/proc/%d/fd", pid)
	fds, err := os.ReadDir(fdDir)
	if err != nil {
		return false
	}
	for _, fd := range fds {
		target, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
		if err != nil {
			continue
		}
		if PathHoldsVolumeTree(target, trees) {
			return true
		}
	}
	return false
}
