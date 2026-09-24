package edgelet

import (
	"os"
	"path/filepath"
	"strings"
)

// rebuildableVolumeStagingTargets lists per-microservice staging directories
// under volumes/microservices that are not in the keep-set. Persistent VOLUME
// trees under volumes/data and volumes/shared are never returned.
func rebuildableVolumeStagingTargets(baseVolumesDir string, keepUUIDs map[string]struct{}) ([]string, error) {
	stagingDir := filepath.Join(baseVolumesDir, "microservices")
	entries, err := os.ReadDir(stagingDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	targets := make([]string, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		uuid := strings.TrimSpace(entry.Name())
		if uuid == "" {
			continue
		}
		if _, ok := keepUUIDs[uuid]; ok {
			continue
		}
		targets = append(targets, filepath.Join(stagingDir, uuid))
	}
	return targets, nil
}
