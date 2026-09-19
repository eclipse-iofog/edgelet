package volumemount

import (
	"cmp"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
)

const (
	moduleName          = "VolumeMountManager"
	volumesDir          = "volumes"
	secretsDir          = "secrets"
	configMapsDir       = "configMaps"
	microservicesDir    = "microservices"
	persistentDataDir   = "data"
	persistentSharedDir = "shared"
	maxVersionHistory   = 1 // Keep only current version for simplicity
	dataSymlink         = "..data"

	// Permission modes: internal storage uses 750 (owner+group only); bind-mount
	// targets use 755 dirs and 644 files so non-root container processes can access.
	internalDirMode   = 0750 // #nosec G301 -- internal volume storage (secrets/configMaps)
	bindMountDirMode  = 0755 // #nosec G301 -- bind-mount targets must be world-traversable for non-root uid
	bindMountFileMode = 0644 // #nosec G306 -- bind-mount files readable by non-root containers
)

// persistentVolumeDirSkip is the set of on-disk trees that survive volume-mount
// clear. Persistent VOLUME data is destroyed only by explicit reclaim.
func persistentVolumeDirSkip() map[string]struct{} {
	return map[string]struct{}{
		persistentDataDir:   {},
		persistentSharedDir: {},
	}
}

// VolumeMountType represents the type of volume mount
// VolumeMountType identifies the kind of volume mount.
//
//nolint:revive // exported API
type VolumeMountType string

const (
	VolumeMountTypeSecret       VolumeMountType = "SECRET"
	VolumeMountTypeConfigMap    VolumeMountType = "CONFIGMAP"
	VolumeMountTypeMicroservice VolumeMountType = "MICROSERVICE"
)

// VolumeMountManager manages volume mounts for microservices
// VolumeMountManager manages volume mount lifecycle.
//
//nolint:revive // exported API
type VolumeMountManager struct {
	baseDirectory string
	mountIndex    map[string]any
	indexLock     sync.RWMutex
	typeCache     map[string]VolumeMountType
	typeCacheLock sync.RWMutex
}

var (
	instance *VolumeMountManager
	once     sync.Once
)

// GetInstance returns the singleton VolumeMountManager instance
func GetInstance() *VolumeMountManager {
	once.Do(func() {
		cfg := config.GetInstance()
		baseDir := filepath.Join(cfg.DiskDirectory, volumesDir)
		instance = &VolumeMountManager{
			baseDirectory: baseDir,
			mountIndex:    make(map[string]any),
			typeCache:     make(map[string]VolumeMountType),
		}
		instance.init()
	})
	return instance
}

// init initializes the volume mount manager
func (vmm *VolumeMountManager) init() {
	logging.LogDebug(moduleName, "Initializing volume mount manager")

	// Create volumes directory; 0750 restricts access to owner+group (internal storage)
	initErr := os.MkdirAll(vmm.baseDirectory, internalDirMode)
	if initErr != nil {
		logging.LogError(moduleName, "Error creating volumes directory", initErr)
		return
	}

	vmm.loadFromStore()

	// Rebuild type cache after loading from store
	vmm.rebuildTypeCache()

	logging.LogInfo(moduleName, "Volume mount manager initialized successfully")
}

// loadFromStore loads the in-memory mount index from controller_volume_mounts SQLite.
// Falls back to an empty mount index if the store is not yet open.
func (vmm *VolumeMountManager) loadFromStore() {
	vmm.indexLock.Lock()
	defer vmm.indexLock.Unlock()

	db := store.GetInstance()
	if db.Conn() == nil {
		logging.LogWarn(moduleName, "SQLite not open during loadFromStore — starting with empty mount index")
		vmm.mountIndex = make(map[string]any)
		return
	}

	records, err := db.LoadAllControllerVolumeMounts()
	if err != nil {
		logging.LogError(moduleName, "Error loading volume mounts from SQLite", err)
		vmm.mountIndex = make(map[string]any)
		return
	}

	vmm.mountIndex = make(map[string]any, len(records))
	for _, rec := range records {
		typeStr := kindToTypeString(rec.Kind)
		entry := map[string]any{
			"name":          rec.Name,
			"type":          typeStr,
			"version":       rec.Version,
			"checksum":      rec.Checksum,
			"data":          rec.Data,
			"microservices": stringsToInterfaceSlice(rec.Microservices),
		}
		vmm.mountIndex[rec.UUID] = entry

		// Rehydrate main directory structure from SQLite if it does not exist
		vmm.rehydrateMainStructureFromRecord(&rec)
	}

	logging.LogDebug(moduleName, fmt.Sprintf("Loaded %d volume mounts from SQLite", len(vmm.mountIndex)))
}

// rehydrateMainStructureFromRecord creates the main directory structure on disk from a SQLite record
// when it does not exist (e.g. after agent restart with controller unreachable).
func (vmm *VolumeMountManager) rehydrateMainStructureFromRecord(rec *store.VolumeMountRecord) {
	if len(rec.Data) == 0 {
		return
	}

	vmType := VolumeMountTypeSecret
	if rec.Kind == "CONFIGMAP" {
		vmType = VolumeMountTypeConfigMap
	}

	typeDir := getTypeDirectory(vmType)
	mountPath := filepath.Join(vmm.baseDirectory, typeDir, rec.Name)
	sourceDataLink := filepath.Join(mountPath, dataSymlink)

	if _, err := os.Lstat(sourceDataLink); err == nil {
		// Main structure already exists
		return
	}

	if _, err := vmm.createMainStructureFromData(mountPath, rec.Data, vmType, false); err != nil {
		logging.LogWarn(moduleName, fmt.Sprintf("Rehydration failed for volume mount %s: %v", rec.Name, err))
		return
	}
	logging.LogDebug(moduleName, fmt.Sprintf("Rehydrated main structure for volume mount: %s", rec.Name))
}

// persistToStore persists the entire in-memory mount index to SQLite atomically.
func (vmm *VolumeMountManager) persistToStore() {
	db := store.GetInstance()
	if db.Conn() == nil {
		logging.LogWarn(moduleName, "SQLite not open during persistToStore — skipping")
		return
	}

	records := make([]store.VolumeMountRecord, 0, len(vmm.mountIndex))
	for uuid, mountDataValue := range vmm.mountIndex {
		mountData, ok := mountDataValue.(map[string]any)
		if !ok {
			continue
		}
		name, ok := mountData["name"].(string)
		if !ok {
			name = ""
		}
		typeStr, ok := mountData["type"].(string)
		if !ok {
			typeStr = ""
		}
		version, ok := mountData["version"].(float64)
		if !ok {
			version = 0
		}
		checksum, ok := mountData["checksum"].(string)
		if !ok {
			checksum = ""
		}

		var msSlice []string
		if msRaw, ok := mountData["microservices"].([]any); ok {
			for _, v := range msRaw {
				if s, ok := v.(string); ok {
					msSlice = append(msSlice, s)
				}
			}
		}
		if msSlice == nil {
			msSlice = []string{}
		}

		var dataMap map[string]any
		if d, ok := mountData["data"].(map[string]any); ok {
			dataMap = d
		}
		if dataMap == nil {
			dataMap = map[string]any{}
		}

		records = append(records, store.VolumeMountRecord{
			UUID:          uuid,
			Name:          name,
			Kind:          typeStringToKind(typeStr),
			Version:       version,
			Checksum:      checksum,
			Microservices: msSlice,
			Data:          dataMap,
		})
	}

	if err := db.ReplaceAllControllerVolumeMounts(records); err != nil {
		logging.LogError(moduleName, "Error saving volume mounts to SQLite", err)
	}

	activeMounts := int64(len(vmm.mountIndex))
	statusreporter.GetInstance().UpdateVolumeMountManagerStatus(activeMounts, time.Now().UnixMilli())
}

// checksum computes SHA256 checksum of a string
func (vmm *VolumeMountManager) checksum(data string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(data))
	return fmt.Sprintf("%x", h.Sum(nil))
}

// rebuildTypeCache rebuilds the type cache from the mount index
// Acquires indexLock - use rebuildTypeCacheUnsafe if lock is already held
func (vmm *VolumeMountManager) rebuildTypeCache() {
	vmm.indexLock.RLock()
	defer vmm.indexLock.RUnlock()
	vmm.rebuildTypeCacheUnsafe()
}

// rebuildTypeCacheUnsafe rebuilds the type cache without acquiring indexLock
// Caller must hold indexLock (read or write)
func (vmm *VolumeMountManager) rebuildTypeCacheUnsafe() {
	vmm.typeCacheLock.Lock()
	defer vmm.typeCacheLock.Unlock()

	vmm.typeCache = make(map[string]VolumeMountType)

	for _, mountDataValue := range vmm.mountIndex {
		mountData, ok := mountDataValue.(map[string]any)
		if !ok {
			continue
		}

		name, ok := mountData["name"].(string)
		if !ok {
			name = ""
		}
		typeStr, ok := mountData["type"].(string)
		if !ok {
			typeStr = ""
		}

		if name != "" {
			if typeStr == "secret" {
				vmm.typeCache[name] = VolumeMountTypeSecret
			} else {
				vmm.typeCache[name] = VolumeMountTypeConfigMap
			}
		}
	}
}

// GetVolumeMountType gets the volume mount type by name (from cache)
func (vmm *VolumeMountManager) GetVolumeMountType(volumeName string) VolumeMountType {
	vmm.typeCacheLock.RLock()
	defer vmm.typeCacheLock.RUnlock()
	return vmm.typeCache[volumeName]
}

// ProcessVolumeMountChanges processes volume mount changes from controller
// handles errors gracefully
func (vmm *VolumeMountManager) ProcessVolumeMountChanges(volumeMounts []any) {
	// Use defer/recover to catch any panics
	defer func() {
		if r := recover(); r != nil {
			logging.LogError(moduleName, fmt.Sprintf("Panic in ProcessVolumeMountChanges: %v", r), fmt.Errorf("%v", r))
		}
	}()

	vmm.indexLock.Lock()
	defer vmm.indexLock.Unlock()

	logging.LogInfo(moduleName, "Processing volume mount changes")

	// Get existing volume mount UUIDs
	existingUuids := make(map[string]bool)
	for uuid := range vmm.mountIndex {
		existingUuids[uuid] = true
	}

	// Get new volume mount UUIDs
	newUuids := make(map[string]bool)
	volumeMountMap := make(map[string]any)
	for _, vm := range volumeMounts {
		vmMap, ok := vm.(map[string]any)
		if !ok {
			continue
		}
		uuid, ok := vmMap["uuid"].(string)
		if !ok {
			uuid = ""
		}
		if uuid != "" {
			newUuids[uuid] = true
			volumeMountMap[uuid] = vmMap
		}
	}

	// Handle removed volume mounts
	for uuid := range existingUuids {
		if !newUuids[uuid] {
			func() {
				defer func() {
					if r := recover(); r != nil {
						logging.LogError(moduleName, fmt.Sprintf("Panic in deleteVolumeMount(%s): %v", uuid, r), fmt.Errorf("%v", r))
					}
				}()
				vmm.deleteVolumeMount(uuid)
			}()
		}
	}

	// Handle new and updated volume mounts
	for uuid, vmValue := range volumeMountMap {
		vmMap, ok := vmValue.(map[string]any)
		if !ok {
			continue
		}

		func() {
			defer func() {
				if r := recover(); r != nil {
					logging.LogError(moduleName, fmt.Sprintf("Panic processing volume mount %s: %v", uuid, r), fmt.Errorf("%v", r))
				}
			}()

			if existingUuids[uuid] {
				logging.LogDebug(moduleName, fmt.Sprintf("Updating volume mount: %s", uuid))
				vmm.updateVolumeMount(vmMap)
			} else {
				logging.LogDebug(moduleName, fmt.Sprintf("Creating new volume mount: %s", uuid))
				vmm.createVolumeMount(vmMap)
			}
		}()
	}

	// Rebuild type cache after updates (with error handling)
	// Note: We already hold indexLock, so rebuildTypeCacheUnsafe is used
	func() {
		defer func() {
			if r := recover(); r != nil {
				logging.LogError(moduleName, fmt.Sprintf("Panic in rebuildTypeCache: %v", r), fmt.Errorf("%v", r))
			}
		}()
		vmm.rebuildTypeCacheUnsafe() // Use unsafe version since we already hold the lock
	}()

	// Persist updated mount index (with error handling)
	func() {
		defer func() {
			if r := recover(); r != nil {
				logging.LogError(moduleName, fmt.Sprintf("Panic in persistToStore: %v", r), fmt.Errorf("%v", r))
			}
		}()
		vmm.persistToStore()
	}()

	logging.LogInfo(moduleName, "Volume mount changes processed successfully")
}

// Helper functions

// kindToTypeString converts a SQLite Kind value ("SECRET"/"CONFIGMAP") to the
// in-memory type string ("secret"/"configMap") used throughout manager.go.
func kindToTypeString(kind string) string {
	if kind == "CONFIGMAP" {
		return "configMap"
	}
	return "secret"
}

// typeStringToKind converts the in-memory type string to the SQLite Kind value.
func typeStringToKind(typeStr string) string {
	if typeStr == "configMap" {
		return "CONFIGMAP"
	}
	return "SECRET"
}

// stringsToInterfaceSlice converts []string to []any for mountIndex compatibility.
func stringsToInterfaceSlice(ss []string) []any {
	result := make([]any, len(ss))
	for i, s := range ss {
		result[i] = s
	}
	return result
}

func decodeBase64(data string) (string, error) {
	// Remove quotes if present
	data = strings.Trim(data, "\"")
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

// createVersionedDirectoryName creates a versioned directory name
func createVersionedDirectoryName() string {
	now := time.Now()
	timestamp := now.Format("2006_01_02_15_04_05")
	nanoseconds := now.Nanosecond() % 1_000_000_000
	return fmt.Sprintf("..%s.%09d", timestamp, nanoseconds)
}

// getTypeDirectory gets the directory name for a volume mount type
func getTypeDirectory(vmType VolumeMountType) string {
	if vmType == VolumeMountTypeSecret {
		return secretsDir
	}
	return configMapsDir
}

// parseVolumeMountType parses volume mount type from map
func parseVolumeMountType(vmMap map[string]any) VolumeMountType {
	typeStr, ok := vmMap["type"].(string)
	if !ok {
		typeStr = ""
	}
	if typeStr == "secret" {
		return VolumeMountTypeSecret
	}
	return VolumeMountTypeConfigMap
}

// dirMode returns the directory permission mode for internal volume storage.
// Internal dirs (secrets/, configMaps/, version dirs) use 0750 (owner+group only).
// Bind-mount targets use bindMountDirMode (0755) so non-root containers can traverse.
func dirMode(_ VolumeMountType) os.FileMode {
	return internalDirMode
}

// fileMode returns the file permission mode for the given volume mount type.
// Secrets use 0600 (owner-only), configMaps use 0644 (world-readable).
func fileMode(vmType VolumeMountType) os.FileMode {
	if vmType == VolumeMountTypeSecret {
		return 0600
	}
	return 0644 // #nosec G306 -- configmap files are intentionally world-readable
}

// setFilePermissions sets file permissions based on volume mount type
func setFilePermissions(path string, vmType VolumeMountType) error {
	if runtime.GOOS == "windows" {
		// Windows: set read-only for secrets
		if vmType == VolumeMountTypeSecret {
			return os.Chmod(path, 0400)
		}
		return os.Chmod(path, 0444) // #nosec G302 -- configmap files on Windows are intentionally readable
	}

	// POSIX: secrets get 600, configMaps get 644
	return os.Chmod(path, fileMode(vmType)) // #nosec G302 -- configmap files are intentionally world-readable; secrets get 0600
}

// setSymlinkPermissions sets symlink permissions to 777 (traversal permissions)
func setSymlinkPermissions(symlinkPath string) error {
	if runtime.GOOS == "windows" {
		return nil // Windows doesn't support symlink permissions the same way
	}
	return os.Chmod(symlinkPath, 0777) // #nosec G302 -- symlinks need 777 for traversal; actual data permissions are set on target files
}

// buildRelativeDataTarget computes the relative symlink target for a nested key path.
// For a key "dir/file", the symlink at mountPath/dir/file must point to ../..data/dir/file.
// For a flat key "mykey", the symlink at mountPath/mykey points to ..data/mykey.
func buildRelativeDataTarget(keyLink string) string {
	depth := strings.Count(keyLink, "/")
	prefix := strings.Repeat("../", depth)
	return prefix + dataSymlink + "/" + keyLink
}

// copyVersionedDirRecursively recursively copies from internal storage to a bind-mount target.
// dirs: 0755 (world-traversable for non-root containers); files: 0644 (caller passes bindMountFileMode).
func copyVersionedDirRecursively(src, dst string, fileMode os.FileMode) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		targetPath := filepath.Join(dst, relPath)

		if info.IsDir() {
			return os.MkdirAll(targetPath, bindMountDirMode)
		}

		data, err := os.ReadFile(path) // #nosec G304,G122 -- path is from filepath.Walk over a known system directory
		if err != nil {
			return err
		}

		if err := os.MkdirAll(filepath.Dir(targetPath), bindMountDirMode); err != nil {
			return err
		}

		if err := os.WriteFile(targetPath, data, fileMode); err != nil { // #nosec G306,G703 -- bind-mount files 0644; targetPath under controlled dst from Walk
			return err
		}

		return os.Chmod(targetPath, fileMode) // #nosec G302 -- bind-mount files 0644
	})
}

// createKeySymlinksRecursively walks versionedDir and for each file creates a
// corresponding symlink at mountPath/<relPath> → buildRelativeDataTarget(relPath).
func createKeySymlinksRecursively(mountPath, versionedDir string) error {
	return filepath.Walk(versionedDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(versionedDir, path)
		if err != nil {
			return err
		}

		// Use forward slashes for the key path (symlink target computation)
		relPathFwd := filepath.ToSlash(relPath)
		linkTarget := buildRelativeDataTarget(relPathFwd)

		symlinkPath := filepath.Join(mountPath, relPath)
		// Bind-mount dirs need 0755 for non-root container traversal
		symlinkDirErr := os.MkdirAll(filepath.Dir(symlinkPath), bindMountDirMode)
		if symlinkDirErr != nil {
			return symlinkDirErr
		}

		// Remove any existing symlink/file at this path
		_ = os.Remove(symlinkPath)

		if err := os.Symlink(linkTarget, symlinkPath); err != nil { // #nosec G122 -- paths under controlled versionedDir/mountPath
			return err
		}

		return setSymlinkPermissions(symlinkPath)
	})
}

// removeObsoleteSymlinksRecursively walks mountPath and removes symlinks whose
// target files no longer exist in targetVersionedDir. Removes empty directories bottom-up.
func removeObsoleteSymlinksRecursively(mountPath, targetVersionedDir string) error {
	var dirsToCheck []string

	err := filepath.Walk(mountPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil //nolint:nilerr // continue walking past individual entry errors
		}
		if path == mountPath {
			return nil
		}

		if info.IsDir() {
			dirsToCheck = append(dirsToCheck, path)
			return nil
		}

		// Skip the ..data symlink and versioned dirs
		name := filepath.Base(path)
		if strings.HasPrefix(name, "..") {
			return nil
		}

		linfo, lerr := os.Lstat(path)
		if lerr != nil {
			return nil //nolint:nilerr // continue walking past individual entry errors
		}
		if linfo.Mode()&os.ModeSymlink == 0 {
			return nil
		}

		// Compute the corresponding file in the versioned dir
		relPath, rerr := filepath.Rel(mountPath, path)
		if rerr != nil {
			return nil //nolint:nilerr // continue walking past individual entry errors
		}

		targetFile := filepath.Join(targetVersionedDir, relPath)
		if _, serr := os.Stat(targetFile); os.IsNotExist(serr) { // #nosec G122 -- paths under controlled mountPath/versionedDir
			_ = os.Remove(path)
		}

		return nil
	})

	if err != nil {
		return err
	}

	// Remove empty directories bottom-up (longest path first)
	slices.SortFunc(dirsToCheck, func(a, b string) int {
		return cmp.Compare(b, a)
	})
	for _, dir := range dirsToCheck {
		if dir == mountPath {
			continue
		}
		entries, rerr := os.ReadDir(dir)
		if rerr == nil && len(entries) == 0 {
			_ = os.Remove(dir)
		}
	}

	return nil
}

// createMainStructureFromData creates the main directory structure (versioned dir, files, ..data symlink, key symlinks)
// from the given data map (key->base64). Used by createVolumeMount, updateVolumeMount, and rehydration.
// If atomicSymlink is true, creates the ..data symlink atomically (write to .tmp then rename).
func (vmm *VolumeMountManager) createMainStructureFromData(mountPath string, dataObj map[string]any, vmType VolumeMountType, atomicSymlink bool) (versionDirName string, err error) {
	if err := os.MkdirAll(mountPath, dirMode(vmType)); err != nil {
		return "", fmt.Errorf("create mount directory: %w", err)
	}

	versionDirName = createVersionedDirectoryName()
	versionDir := filepath.Join(mountPath, versionDirName)
	if err := os.MkdirAll(versionDir, dirMode(vmType)); err != nil {
		return "", fmt.Errorf("create versioned directory: %w", err)
	}

	for key, value := range dataObj {
		valueStr, ok := value.(string)
		if !ok {
			continue
		}

		decodedContent, decodeErr := decodeBase64(valueStr)
		if decodeErr != nil {
			logging.LogError(moduleName, fmt.Sprintf("Error decoding base64 for key: %s", key), decodeErr)
			continue
		}

		filePath := filepath.Join(versionDir, key)
		if err := os.MkdirAll(filepath.Dir(filePath), dirMode(vmType)); err != nil {
			return "", fmt.Errorf("create parent for %s: %w", key, err)
		}

		if err := os.WriteFile(filePath, []byte(decodedContent), fileMode(vmType)); err != nil {
			return "", fmt.Errorf("write file %s: %w", key, err)
		}

		if err := setFilePermissions(filePath, vmType); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("Error setting file permissions for: %s: %v", key, err))
		}
	}

	dataLink := filepath.Join(mountPath, dataSymlink)
	symlinkTarget := dataLink
	if atomicSymlink {
		symlinkTarget = filepath.Join(mountPath, dataSymlink+".tmp")
		if err := os.Remove(symlinkTarget); err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("remove temp data symlink: %w", err)
		}
	} else {
		if err := os.Remove(dataLink); err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("remove existing data symlink: %w", err)
		}
	}
	if err := os.Symlink(versionDirName, symlinkTarget); err != nil {
		return "", fmt.Errorf("create data symlink: %w", err)
	}
	if err := setSymlinkPermissions(symlinkTarget); err != nil {
		logging.LogWarn(moduleName, fmt.Sprintf("Error setting data symlink permissions: %v", err))
	}
	if atomicSymlink {
		if err := os.Rename(symlinkTarget, dataLink); err != nil {
			return "", fmt.Errorf("atomic rename data symlink: %w", err)
		}
	}

	if err := createKeySymlinksRecursively(mountPath, versionDir); err != nil {
		return "", fmt.Errorf("create key symlinks: %w", err)
	}

	return versionDirName, nil
}

// createVolumeMount creates a new volume mount
func (vmm *VolumeMountManager) createVolumeMount(vmMap map[string]any) {
	uuid, ok := vmMap["uuid"].(string)
	if !ok {
		uuid = ""
	}
	name, ok := vmMap["name"].(string)
	if !ok {
		name = ""
	}
	version, ok := vmMap["version"].(float64)
	if !ok {
		version = 0
	}
	dataObj, ok := vmMap["data"].(map[string]any)
	if !ok {
		dataObj = map[string]any{}
	}
	vmType := parseVolumeMountType(vmMap)

	logging.LogDebug(moduleName, fmt.Sprintf("Creating volume mount - UUID: %s, Name: %s, Version: %.0f, Type: %s",
		uuid, name, version, vmType))

	typeDir := getTypeDirectory(vmType)
	mountPath := filepath.Join(vmm.baseDirectory, typeDir, name)
	if _, err := vmm.createMainStructureFromData(mountPath, dataObj, vmType, false); err != nil {
		logging.LogError(moduleName, fmt.Sprintf("Error creating main structure: %v", err), err)
		return
	}

	// Compute checksum from the raw data payload (base64 values from controller)
	dataChecksumJSON, _ := json.Marshal(dataObj)
	dataChecksum := vmm.checksum(string(dataChecksumJSON))

	// Update index with new schema; store raw content (key->base64) for rehydration
	mountData := map[string]any{
		"name":          name,
		"type":          map[string]string{string(VolumeMountTypeSecret): "secret", string(VolumeMountTypeConfigMap): "configMap"}[string(vmType)],
		"version":       version,
		"checksum":      dataChecksum,
		"data":          dataObj,
		"microservices": []any{},
	}

	vmm.mountIndex[uuid] = mountData
	vmm.persistToStore()

	logging.LogDebug(moduleName, fmt.Sprintf("Volume mount created successfully: %s", uuid))
}

// updateVolumeMount updates an existing volume mount
func (vmm *VolumeMountManager) updateVolumeMount(vmMap map[string]any) {
	uuid, ok := vmMap["uuid"].(string)
	if !ok {
		uuid = ""
	}
	name, ok := vmMap["name"].(string)
	if !ok {
		name = ""
	}
	version, ok := vmMap["version"].(float64)
	if !ok {
		version = 0
	}
	dataObj, ok := vmMap["data"].(map[string]any)
	if !ok {
		dataObj = map[string]any{}
	}
	vmType := parseVolumeMountType(vmMap)

	logging.LogDebug(moduleName, fmt.Sprintf("Updating volume mount - UUID: %s, Name: %s, Version: %.0f, Type: %s",
		uuid, name, version, vmType))

	// Get current version from index
	currentMount, ok := vmm.mountIndex[uuid].(map[string]any)
	if ok {
		currentVersion, ok := currentMount["version"].(float64)
		if !ok {
			currentVersion = 0
		}
		if version <= currentVersion {
			logging.LogWarn(moduleName, fmt.Sprintf("Skipping update - new version %.0f not greater than current version %.0f",
				version, currentVersion))
			return
		}
	}

	// Get old keys for deletion
	oldKeys := make(map[string]bool)
	if currentMount != nil {
		if oldData, ok := currentMount["data"].(map[string]any); ok {
			for key := range oldData {
				oldKeys[key] = true
			}
		}
	}

	newKeys := make(map[string]bool)
	for key := range dataObj {
		newKeys[key] = true
	}

	// Find deleted keys
	deletedKeys := make(map[string]bool)
	for key := range oldKeys {
		if !newKeys[key] {
			deletedKeys[key] = true
		}
	}

	typeDir := getTypeDirectory(vmType)
	mountPath := filepath.Join(vmm.baseDirectory, typeDir, name)
	versionDirName, err := vmm.createMainStructureFromData(mountPath, dataObj, vmType, true)
	if err != nil {
		logging.LogError(moduleName, fmt.Sprintf("Error creating main structure: %v", err), err)
		return
	}
	versionDir := filepath.Join(mountPath, versionDirName)

	// Remove obsolete symlinks for keys no longer present
	if err := removeObsoleteSymlinksRecursively(mountPath, versionDir); err != nil {
		logging.LogWarn(moduleName, fmt.Sprintf("Error removing obsolete symlinks: %v", err))
	}

	// Clean up old version directories
	vmm.cleanupOldVersions(mountPath)

	// Get existing microservices list
	microservicesArray := []any{}
	if currentMount != nil {
		if ms, ok := currentMount["microservices"].([]any); ok {
			microservicesArray = ms
		}
	}

	// Compute checksum from the raw data payload (base64 values from controller)
	dataChecksumJSON, _ := json.Marshal(dataObj)
	dataChecksum := vmm.checksum(string(dataChecksumJSON))

	// Update index with new schema; store raw content (key->base64) for rehydration
	mountData := map[string]any{
		"name":          name,
		"type":          map[string]string{string(VolumeMountTypeSecret): "secret", string(VolumeMountTypeConfigMap): "configMap"}[string(vmType)],
		"version":       version,
		"checksum":      dataChecksum,
		"data":          dataObj,
		"microservices": microservicesArray,
	}

	vmm.mountIndex[uuid] = mountData
	vmm.persistToStore()

	// Sync symlinks in per-microservice directories to reflect the update.
	// ProcessVolumeMountChanges already holds indexLock; use Unsafe to avoid re-entrant deadlock.
	vmm.syncMicroserviceSymlinksUnsafe(name, vmType)

	logging.LogDebug(moduleName, fmt.Sprintf("Volume mount updated successfully: %s", uuid))
}

// deleteVolumeMount deletes a volume mount
func (vmm *VolumeMountManager) deleteVolumeMount(uuid string) {
	logging.LogDebug(moduleName, fmt.Sprintf("Deleting volume mount: %s", uuid))

	// Get mount info from index
	mountData, ok := vmm.mountIndex[uuid].(map[string]any)
	if !ok {
		logging.LogWarn(moduleName, fmt.Sprintf("Volume mount not found: %s", uuid))
		return
	}

	// Delete mount directory and files
	name, ok := mountData["name"].(string)
	if !ok {
		name = ""
	}
	typeStr, ok := mountData["type"].(string)
	if !ok {
		typeStr = ""
	}
	typeDir := secretsDir
	if typeStr == "configMap" {
		typeDir = configMapsDir
	}

	mountPath := filepath.Join(vmm.baseDirectory, typeDir, name)
	if _, err := os.Stat(mountPath); err == nil {
		if err := os.RemoveAll(mountPath); err != nil {
			logging.LogError(moduleName, fmt.Sprintf("Error deleting mount directory: %s", mountPath), err)
		}
	}

	// Remove from index
	delete(vmm.mountIndex, uuid)

	// Remove from type cache
	vmm.typeCacheLock.Lock()
	delete(vmm.typeCache, name)
	vmm.typeCacheLock.Unlock()

	vmm.persistToStore()
	logging.LogDebug(moduleName, fmt.Sprintf("Volume mount deleted successfully: %s", uuid))
}

// cleanupOldVersions cleans up old version directories, keeping only the last MAX_VERSION_HISTORY versions
func (vmm *VolumeMountManager) cleanupOldVersions(mountPath string) {
	entries, err := os.ReadDir(mountPath)
	if err != nil {
		logging.LogWarn(moduleName, fmt.Sprintf("Error reading mount path: %s: %v", mountPath, err))
		return
	}

	var versionDirs []os.DirEntry
	for _, entry := range entries {
		name := entry.Name()
		// Check if it's a versioned directory (starts with .. and matches pattern)
		if strings.HasPrefix(name, "..") && len(name) > 2 {
			// Simple pattern check: .. followed by timestamp pattern
			if entry.IsDir() {
				versionDirs = append(versionDirs, entry)
			}
		}
	}

	if len(versionDirs) <= maxVersionHistory {
		return
	}
	// Sort by modification time (oldest first)
	slices.SortStableFunc(versionDirs, func(a, b os.DirEntry) int {
		infoA, errA := a.Info()
		infoB, errB := b.Info()
		if errA != nil || errB != nil {
			return 0
		}
		if infoA.ModTime().Before(infoB.ModTime()) {
			return -1
		}
		if infoA.ModTime().After(infoB.ModTime()) {
			return 1
		}
		return 0
	})

	// Delete old versions, keeping last MAX_VERSION_HISTORY
	toDelete := len(versionDirs) - maxVersionHistory
	for i := 0; i < toDelete && i < len(versionDirs); i++ {
		versionPath := filepath.Join(mountPath, versionDirs[i].Name())
		if err := os.RemoveAll(versionPath); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("Error deleting old version: %s: %v", versionPath, err))
		}
	}
}

// getTypePrefix gets the type prefix for per-microservice directories
func getTypePrefix(vmType VolumeMountType) string {
	if vmType == VolumeMountTypeSecret {
		return "edgelet.iofog.org~secret"
	}
	return "edgelet.iofog.org~configmap"
}

// getMountPath gets the mount path for a microservice volume mount
func (vmm *VolumeMountManager) getMountPath(microserviceUUID, volumeName string, vmType VolumeMountType) string {
	typePrefix := getTypePrefix(vmType)
	return filepath.Join(vmm.baseDirectory, microservicesDir, microserviceUUID, "volumes", typePrefix, volumeName)
}

// PrepareMicroserviceVolumeMount prepares per-microservice volume mount directory with symlinks
func (vmm *VolumeMountManager) PrepareMicroserviceVolumeMount(microserviceUUID, volumeName string, vmType VolumeMountType) string {
	mountPath := vmm.getMountPath(microserviceUUID, volumeName, vmType)

	// Slow path: create directory and copy files
	// Log error but don't fail container creation
	defer func() {
		if r := recover(); r != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("Error in PrepareMicroserviceVolumeMount: %v", r))
		}
	}()

	// Atomic directory creation (handles race conditions)
	// Bind-mount targets use 0755 so non-root container processes can traverse
	if err := os.MkdirAll(mountPath, bindMountDirMode); err != nil {
		logging.LogWarn(moduleName, fmt.Sprintf("Error creating microservice mount directory: %v", err))
		return mountPath // Return path anyway
	}

	// Calculate source path
	typeDir := getTypeDirectory(vmType)
	sourcePath := filepath.Join(vmm.baseDirectory, typeDir, volumeName)
	sourceDataLink := filepath.Join(sourcePath, dataSymlink)

	// Resolve the actual ..data symlink to get the real versioned directory
	sourceVersionedDirPath, err := filepath.EvalSymlinks(sourceDataLink)
	if err != nil {
		if !os.IsNotExist(err) {
			logging.LogWarn(moduleName, fmt.Sprintf("Source ..data symlink does not exist for: %s", volumeName))
		}
		return mountPath // Return path anyway
	}

	// Get the versioned directory name (e.g., ..2025_12_30_15_00_00.123456789)
	versionedDirName := filepath.Base(sourceVersionedDirPath)

	// Fast path: skip if ..data already points to the correct versioned dir.
	// Still ensure both the mount directory and the versioned directory have 0755
	// permissions so that existing volumes created with the old 0700 mode are
	// migrated transparently (non-root container processes must be able to traverse).
	dataLink := filepath.Join(mountPath, dataSymlink)
	if currentTarget, err := os.Readlink(dataLink); err == nil {
		if currentTarget == versionedDirName {
			_ = os.Chmod(mountPath, bindMountDirMode)                                  // #nosec G302 -- migrate legacy 0700 dirs
			_ = os.Chmod(filepath.Join(mountPath, versionedDirName), bindMountDirMode) // #nosec G302 -- migrate legacy 0700 dirs
			return mountPath                                                           // already up to date
		}
	}

	// Copy the versioned directory to per-microservice directory (recursively)
	// Bind-mount files use 0644 so non-root containers can read both secrets and configmaps
	targetVersionedDir := filepath.Join(mountPath, versionedDirName)
	if _, err := os.Stat(targetVersionedDir); os.IsNotExist(err) {
		if err := copyVersionedDirRecursively(sourceVersionedDirPath, targetVersionedDir, bindMountFileMode); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("Error copying versioned directory: %v", err))
			return mountPath
		}
	}

	// Create or update ..data symlink pointing to versioned directory (relative path)
	if _, err := os.Lstat(dataLink); err == nil {
		// Remove stale symlink before recreating
		if err := os.Remove(dataLink); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("Error removing stale data symlink: %v", err))
		}
	}
	if _, err := os.Lstat(dataLink); os.IsNotExist(err) {
		if err := os.Symlink(versionedDirName, dataLink); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("Error creating data symlink: %v", err))
		} else {
			if err := setSymlinkPermissions(dataLink); err != nil {
				logging.LogWarn(moduleName, fmt.Sprintf("Error setting data symlink permissions: %v", err))
			}
		}
	}

	// Create key symlinks recursively (supports nested paths like dir/subdir/file)
	if err := createKeySymlinksRecursively(mountPath, targetVersionedDir); err != nil {
		logging.LogWarn(moduleName, fmt.Sprintf("Error creating key symlinks: %v", err))
	}

	// Track microservice usage in index
	vmm.trackMicroserviceUsage(volumeName, microserviceUUID, true)

	return mountPath
}

// trackMicroserviceUsage tracks microservice usage of volume mounts in index.
func (vmm *VolumeMountManager) trackMicroserviceUsage(volumeName, microserviceUUID string, add bool) {
	vmm.indexLock.Lock()
	defer vmm.indexLock.Unlock()
	vmm.trackMicroserviceUsageUnsafe(volumeName, microserviceUUID, add)
}

// trackMicroserviceUsageUnsafe is the lock-free variant of trackMicroserviceUsage.
// Caller must already hold indexLock (read or write).
func (vmm *VolumeMountManager) trackMicroserviceUsageUnsafe(volumeName, microserviceUUID string, add bool) {
	for uuid, mountDataValue := range vmm.mountIndex {
		mountData, ok := mountDataValue.(map[string]any)
		if !ok {
			continue
		}

		name, ok := mountData["name"].(string)
		if !ok {
			name = ""
		}
		if name != volumeName {
			continue
		}

		// Get existing microservices array
		microservicesArray := []any{}
		if ms, ok := mountData["microservices"].([]any); ok {
			microservicesArray = ms
		}

		// Check if microservice is already in list
		found := false
		for _, ms := range microservicesArray {
			if msStr, ok := ms.(string); ok && msStr == microserviceUUID {
				found = true
				break
			}
		}

		if add && !found {
			microservicesArray = append(microservicesArray, microserviceUUID)
		} else if !add && found {
			// Remove microservice from list
			newArray := []any{}
			for _, ms := range microservicesArray {
				if msStr, ok := ms.(string); ok && msStr != microserviceUUID {
					newArray = append(newArray, ms)
				}
			}
			microservicesArray = newArray
		}

		// Update mount data with new microservices list
		newMountData := make(map[string]any)
		for k, v := range mountData {
			newMountData[k] = v
		}
		newMountData["microservices"] = microservicesArray

		// Update index (using uuid to update the correct entry)
		vmm.mountIndex[uuid] = newMountData
		break
	}
}

// GetVolumeMountByName gets volume mount info by name
func (vmm *VolumeMountManager) GetVolumeMountByName(volumeName string) map[string]any {
	vmm.indexLock.RLock()
	defer vmm.indexLock.RUnlock()

	for _, mountDataValue := range vmm.mountIndex {
		mountData, ok := mountDataValue.(map[string]any)
		if !ok {
			continue
		}

		name, ok := mountData["name"].(string)
		if !ok {
			name = ""
		}
		if name == volumeName {
			return mountData
		}
	}
	return nil
}

// syncMicroserviceSymlinks syncs per-microservice symlinks when source volume mount is updated
func (vmm *VolumeMountManager) syncMicroserviceSymlinks(volumeName string, vmType VolumeMountType) {
	vmm.indexLock.Lock()
	defer vmm.indexLock.Unlock()
	vmm.syncMicroserviceSymlinksUnsafe(volumeName, vmType)
}

// syncMicroserviceSymlinksUnsafe is the lock-free variant of syncMicroserviceSymlinks.
// Caller must already hold indexLock (read or write).
func (vmm *VolumeMountManager) syncMicroserviceSymlinksUnsafe(volumeName string, vmType VolumeMountType) {
	var volumeMountData map[string]any
	for _, mountDataValue := range vmm.mountIndex {
		mountData, ok := mountDataValue.(map[string]any)
		if !ok {
			continue
		}
		name, ok := mountData["name"].(string)
		if !ok {
			name = ""
		}
		if name == volumeName {
			volumeMountData = mountData
			break
		}
	}
	if volumeMountData == nil {
		return
	}

	microservicesArray, ok := volumeMountData["microservices"].([]any)
	if !ok || len(microservicesArray) == 0 {
		return
	}

	// Get the source versioned directory
	typeDir := getTypeDirectory(vmType)
	sourcePath := filepath.Join(vmm.baseDirectory, typeDir, volumeName)
	sourceDataLink := filepath.Join(sourcePath, dataSymlink)

	// Resolve the actual ..data symlink to get the real versioned directory
	sourceVersionedDirPath, err := filepath.EvalSymlinks(sourceDataLink)
	if err != nil {
		return
	}

	versionedDirName := filepath.Base(sourceVersionedDirPath)

	// Update symlinks for each microservice using this volume mount
	for _, msValue := range microservicesArray {
		microserviceUUID, ok := msValue.(string)
		if !ok {
			continue
		}

		mountPath := vmm.getMountPath(microserviceUUID, volumeName, vmType)

		if _, err := os.Stat(mountPath); os.IsNotExist(err) {
			continue
		}

		// Copy new versioned directory recursively if it doesn't exist
		// Bind-mount files use 0644 so non-root containers can read both secrets and configmaps
		targetVersionedDir := filepath.Join(mountPath, versionedDirName)
		if _, err := os.Stat(targetVersionedDir); os.IsNotExist(err) {
			if err := copyVersionedDirRecursively(sourceVersionedDirPath, targetVersionedDir, bindMountFileMode); err != nil {
				logging.LogWarn(moduleName, fmt.Sprintf("Error copying versioned directory for microservice %s: %v", microserviceUUID, err))
				continue
			}
		}

		// Atomically update ..data symlink to point to new version
		dataLink := filepath.Join(mountPath, dataSymlink)
		newDataLink := filepath.Join(mountPath, dataSymlink+".tmp")

		if rerr := os.Remove(newDataLink); rerr != nil && !os.IsNotExist(rerr) {
			logging.LogWarn(moduleName, fmt.Sprintf("Failed to remove temp data symlink: %v", rerr))
		}

		if err := os.Symlink(versionedDirName, newDataLink); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("Error creating temp data symlink: %v", err))
			continue
		}
		if err := setSymlinkPermissions(newDataLink); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("Error setting temp symlink permissions: %v", err))
		}

		if err := os.Rename(newDataLink, dataLink); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("Error atomically moving data symlink: %v", err))
			continue
		}

		// Update/create key symlinks recursively (supports nested paths)
		if err := createKeySymlinksRecursively(mountPath, targetVersionedDir); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("Error syncing key symlinks for microservice %s: %v", microserviceUUID, err))
		}

		// Remove symlinks for keys that no longer exist
		if err := removeObsoleteSymlinksRecursively(mountPath, targetVersionedDir); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("Error removing obsolete symlinks for microservice %s: %v", microserviceUUID, err))
		}

		// Clean up old versioned directories in per-microservice directory
		vmm.cleanupOldVersions(mountPath)
	}
}

// cleanupMicroserviceVolumesIndexUnsafe removes microserviceUUID from the mount index.
// Caller must hold indexLock (write). Does not touch the filesystem.
func (vmm *VolumeMountManager) cleanupMicroserviceVolumesIndexUnsafe(microserviceUUID string) {
	for _, mountDataValue := range vmm.mountIndex {
		mountData, ok := mountDataValue.(map[string]any)
		if !ok {
			continue
		}

		microservicesArray, ok := mountData["microservices"].([]any)
		if !ok {
			continue
		}

		for _, msValue := range microservicesArray {
			if msStr, ok := msValue.(string); ok && msStr == microserviceUUID {
				volumeName, ok := mountData["name"].(string)
				if !ok {
					volumeName = ""
				}
				vmm.trackMicroserviceUsageUnsafe(volumeName, microserviceUUID, false)
				break
			}
		}
	}
}

// CleanupMicroserviceVolumes cleans up per-microservice volume mount directories.
// Index updates are brief under indexLock; filesystem removal runs without the lock
// so concurrent Clear() filesystem cleanup cannot stall the process manager.
func (vmm *VolumeMountManager) CleanupMicroserviceVolumes(microserviceUUID string) {
	logging.LogDebug(moduleName, fmt.Sprintf("Cleaning up microservice volumes: %s", microserviceUUID))

	microservicePath := filepath.Join(vmm.baseDirectory, microservicesDir, microserviceUUID)
	if _, err := os.Stat(microservicePath); os.IsNotExist(err) {
		return
	}

	vmm.indexLock.Lock()
	vmm.cleanupMicroserviceVolumesIndexUnsafe(microserviceUUID)
	vmm.indexLock.Unlock()

	if err := os.RemoveAll(microservicePath); err != nil {
		logging.LogWarn(moduleName, fmt.Sprintf("Error deleting microservice volume directory: %v", err))
	}

	logging.LogDebug(moduleName, fmt.Sprintf("Microservice volumes cleaned up: %s", microserviceUUID))
}

// parseRunAsUser parses "uid" or "uid:gid" and returns (uid, gid).
// If gid is omitted, gid equals uid. Returns (-1, -1) on parse failure.
func parseRunAsUser(s string) (int, int) {
	s = strings.TrimSpace(s)
	if s == "" {
		return -1, -1
	}
	parts := strings.SplitN(s, ":", 2)
	uid, err := strconv.ParseInt(parts[0], 10, 0)
	if err != nil || uid < 0 {
		return -1, -1
	}
	gid := uid
	if len(parts) == 2 {
		gid, err = strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 0)
		if err != nil || gid < 0 {
			return -1, -1
		}
	}
	return int(uid), int(gid)
}

// ResolveHostPath resolves the absolute host-filesystem path that should be
// bind-mounted into a container for the given volume mapping entry.
//
// For VOLUME_MOUNT type (isVolumeMount == true):
//   - Parses hostDestination as "volumeName" or "volumeName/keyName"
//   - Calls PrepareMicroserviceVolumeMount to create the per-microservice
//     directory tree with versioned data and key symlinks
//   - If a key is specified, appends it to the mount path and resolves any
//     symlinks so that the container gets the real file path
//
// For any non-absolute path (VOLUME named volumes, controller omitting type, etc.):
//   - Private: <diskDirectory>/volumes/data/<uuid>/<name>
//   - Shared: <diskDirectory>/volumes/shared/<name> (same bind for every consumer)
//   - The runtime always receives a writable absolute bind source. Docker and
//     Podman must not treat the volume name as a node-global named volume.
//
// For absolute paths (BIND and explicit host paths):
//   - Returns hostDestination unchanged. scope is ignored.
//
// This is the engine-agnostic equivalent of docker.ResolveVolumeMountPath and
// should be called by every container engine implementation before passing
// mounts to the runtime.
//
// runAsUser is optional; when non-nil and non-empty, private VOLUME dirs are
// chown'd to uid:gid for non-root container access. When runAsUser is empty,
// private VOLUME dirs get 0777. Shared dirs are chowned (or chmod'd) only when
// mkdir created them, so a second consumer cannot steal ownership.
func (vmm *VolumeMountManager) ResolveHostPath(microserviceUUID, hostDestination string, isVolumeMount bool, runAsUser *string, scope string) (string, error) {
	if isVolumeMount {
		var volumeName, keyName string
		slashIdx := strings.Index(hostDestination, "/")
		if slashIdx > 0 {
			volumeName = hostDestination[:slashIdx]
			keyName = hostDestination[slashIdx+1:]
		} else {
			volumeName = hostDestination
		}

		vmType := vmm.GetVolumeMountType(volumeName)
		if vmType == "" {
			// Default to Secret (safer) when type is not in cache, matching Docker's
			// volume.go fallback behavior. A secret with 0600 files is more restrictive
			// than a configMap with 0644 files, avoiding accidental over-permissioning.
			vmType = VolumeMountTypeSecret
		}

		mountPath := vmm.PrepareMicroserviceVolumeMount(microserviceUUID, volumeName, vmType)

		if keyName != "" {
			keyPath := filepath.Join(mountPath, keyName)
			// Resolve symlink to the real versioned file so the runtime sees
			// a concrete path rather than a dangling symlink.
			if resolved, err := filepath.EvalSymlinks(keyPath); err == nil {
				return resolved, nil
			}
			return keyPath, nil
		}

		return mountPath, nil
	}

	// Any remaining non-absolute path must be turned into an absolute host path.
	// This covers:
	//   - VOLUME-type named volumes (private per UUID, or shared by name)
	//   - Mappings whose "type" field was omitted by the controller (zero value "")
	//   - Relative BIND paths (unusual, but must not reach the runtime as-is)
	// A persistent directory is created so the container always gets a real,
	// writable bind-mount source instead of a path relative to the runtime's
	// internal state directory.
	if !filepath.IsAbs(hostDestination) {
		shared := strings.TrimSpace(scope) == models.VolumeScopeShared
		var dir string
		if shared {
			dir = filepath.Join(vmm.baseDirectory, persistentSharedDir, hostDestination)
		} else {
			dir = filepath.Join(vmm.baseDirectory, persistentDataDir, microserviceUUID, hostDestination)
		}
		_, statErr := os.Stat(dir)
		created := os.IsNotExist(statErr)
		mkdirErr := os.MkdirAll(dir, bindMountDirMode)
		if mkdirErr != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("cannot create volume dir %s: %v", dir, mkdirErr))
			created = false
		}
		applyPersistentVolumeDirAccess(dir, runAsUser, shared, created)
		return dir, nil
	}

	return hostDestination, nil
}

func applyPersistentVolumeDirAccess(dir string, runAsUser *string, shared, created bool) {
	if shared && !created {
		return
	}
	if runAsUser != nil && *runAsUser != "" {
		uid, gid := parseRunAsUser(*runAsUser)
		if uid >= 0 {
			if chownErr := os.Chown(dir, uid, gid); chownErr != nil {
				logging.LogWarn(moduleName, fmt.Sprintf("cannot chown volume dir %s to %d:%d: %v", dir, uid, gid, chownErr))
			}
		}
		return
	}
	// RunAsUser empty: make non-root accessible by default (0777).
	// #nosec G302 -- intentional for VOLUME dirs when runAsUser unset
	if chmodErr := os.Chmod(dir, 0777); chmodErr != nil {
		logging.LogWarn(moduleName, fmt.Sprintf("cannot chmod volume dir %s: %v", dir, chmodErr))
	}
}

// clearWalk removes rebuildable volume-mount artifacts under baseDir.
// volumes/data and volumes/shared are left in place so persistent VOLUME
// directories remount after deprovision or re-provision.
func (vmm *VolumeMountManager) clearWalk(baseDir string) {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return
	}
	skip := persistentVolumeDirSkip()
	for _, entry := range entries {
		if _, ok := skip[entry.Name()]; ok {
			continue
		}
		path := filepath.Join(baseDir, entry.Name())
		if err := os.RemoveAll(path); err != nil { // #nosec G122 -- child of controlled baseDir
			logging.LogWarn(moduleName, fmt.Sprintf("Error deleting: %s: %v", path, err))
		}
	}
}

// clearVolumeMountStateLocked resets persisted and in-memory volume mount state.
// Caller must hold indexLock (write). Does not touch the filesystem.
func (vmm *VolumeMountManager) clearVolumeMountStateLocked() {
	if db := store.GetInstance(); db.Conn() != nil {
		if err := db.ClearAllControllerVolumeMounts(); err != nil {
			logging.LogError(moduleName, "Error clearing volume mounts from SQLite", err)
		}
	}

	vmm.mountIndex = make(map[string]any)
	vmm.typeCacheLock.Lock()
	vmm.typeCache = make(map[string]VolumeMountType)
	vmm.typeCacheLock.Unlock()

	statusreporter.GetInstance().UpdateVolumeMountManagerStatus(0, time.Now().UnixMilli())
}

// Clear clears volume-mount index plus secrets, configMaps, and microservice
// staging. It does not walk volumes/data or volumes/shared.
func (vmm *VolumeMountManager) Clear() error {
	logging.LogDebug(moduleName, "Start clearing volume mounts")

	vmm.indexLock.Lock()
	vmm.clearVolumeMountStateLocked()
	vmm.indexLock.Unlock()

	// Filesystem cleanup is intentionally outside indexLock so PM
	// CleanupMicroserviceVolumes can update the index during deprovision.
	if _, err := os.Stat(vmm.baseDirectory); err == nil {
		vmm.clearWalk(vmm.baseDirectory)
		// Retry once after a short sleep to handle races with slow container unmounts
		time.Sleep(100 * time.Millisecond)
		vmm.clearWalk(vmm.baseDirectory)
	}

	logging.LogDebug(moduleName, "Finished clearing volume mounts")
	return nil
}

// ClearControllerArtifacts clears controller-origin volume-mount artifacts
// (secrets and configMaps) while preserving volumes/data and volumes/shared.
func (vmm *VolumeMountManager) ClearControllerArtifacts() error {
	logging.LogDebug(moduleName, "Start clearing controller volume-mount artifacts")

	vmm.indexLock.Lock()
	vmm.clearVolumeMountStateLocked()
	vmm.indexLock.Unlock()

	skip := persistentVolumeDirSkip()
	for _, dirName := range []string{secretsDir, configMapsDir} {
		if _, ok := skip[dirName]; ok {
			continue
		}
		dirPath := filepath.Join(vmm.baseDirectory, dirName)
		if err := os.RemoveAll(dirPath); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("Error deleting controller artifact directory %s: %v", dirPath, err))
		}
	}

	logging.LogDebug(moduleName, "Finished clearing controller volume-mount artifacts")
	return nil
}
