package modelpull

import (
	"path/filepath"
	"time"
)

const (
	OCIStoreDirName = "oci-store"
	ContentDirName  = "content"
	ManifestFile    = "manifest.json"
	// IncompleteExt marks a payload that has not been atomically committed.
	IncompleteExt = ".incomplete"
	// ContentStagingSuffix is the in-progress content directory next to content/.
	ContentStagingSuffix = ".tmp"
	// StaleIncompleteAge is how long a partial download may sit before cleanup.
	StaleIncompleteAge = 7 * 24 * time.Hour
)

// Root returns {diskDirectory}/models.
func Root(diskDirectory string) string {
	return filepath.Join(diskDirectory, "models")
}

// OCIStoreDir returns {modelsRoot}/oci-store.
func OCIStoreDir(modelsRoot string) string {
	return filepath.Join(modelsRoot, OCIStoreDirName)
}

// ModelDir returns {modelsRoot}/{metadata.name}.
func ModelDir(modelsRoot, name string) string {
	return filepath.Join(modelsRoot, name)
}

// ContentDir returns {modelsRoot}/{metadata.name}/content.
func ContentDir(modelsRoot, name string) string {
	return filepath.Join(modelsRoot, name, ContentDirName)
}

// ContentStagingDir returns the in-progress content directory for one model.
func ContentStagingDir(modelsRoot, name string) string {
	return ContentDir(modelsRoot, name) + ContentStagingSuffix
}

// ManifestPath returns {modelsRoot}/{metadata.name}/manifest.json.
func ManifestPath(modelsRoot, name string) string {
	return filepath.Join(modelsRoot, name, ManifestFile)
}
