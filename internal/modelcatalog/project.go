package modelcatalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/modelpull"
	"github.com/eclipse-iofog/edgelet/internal/models"
)

const (
	volumesDir        = "volumes"
	microservicesDir  = "microservices"
	catalogDirName    = "models"
	dataSymlink       = "..data"
	catalogMetaFile   = "..catalog"
	bindMountDirMode  = 0755 // #nosec G301 -- bind-mount parent must be traversable for non-root containers
	bindMountFileMode = 0644 // #nosec G306 -- projected model files are readable inside the container
)

// linkFile is os.Link so tests can force the copy fallback.
var linkFile = os.Link

type snapshot struct {
	BindPath    string         `json:"bindPath"`
	Permissions string         `json:"permissions"`
	Items       []snapshotItem `json:"items"`
}

type snapshotItem struct {
	Name       string `json:"name"`
	Generation int64  `json:"generation"`
}

// ProjectItem is one Ready model to materialize under {name}/.
type ProjectItem struct {
	Name        string
	ContentPath string
	Generation  int64
}

// ProjectResult is the outcome of writing a per-microservice catalog tree.
type ProjectResult struct {
	HostDir      string
	MountChanged bool
}

// HostDir is {diskDirectory}/volumes/microservices/{msUUID}/models.
func HostDir(diskDirectory, msUUID string) string {
	return filepath.Join(strings.TrimSpace(diskDirectory), volumesDir, microservicesDir, strings.TrimSpace(msUUID), catalogDirName)
}

// Cleanup removes the per-microservice catalog projection.
func Cleanup(diskDirectory, msUUID string) error {
	dir := HostDir(diskDirectory, msUUID)
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove catalog directory: %w", err)
	}
	return nil
}

// Project writes {HostDir}/{name}/ from Ready content/ via hardlinks (copy if
// hardlinks are rejected) and an atomic ..data swing. Absolute host content/
// paths are never symlinked into the mount.
func Project(diskDirectory, msUUID string, catalog *models.ModelCatalog, items []ProjectItem) (*ProjectResult, error) {
	if strings.TrimSpace(diskDirectory) == "" {
		return nil, errors.New("disk directory is required")
	}
	msUUID = strings.TrimSpace(msUUID)
	if msUUID == "" {
		return nil, errors.New("microservice uuid is required")
	}
	if catalog == nil {
		catalog = &models.ModelCatalog{}
	}
	cloned := catalog.Clone()
	cloned.NormalizeDefaults()

	hostDir := HostDir(diskDirectory, msUUID)
	mountChanged := models.CatalogMountNeedsRecreate(lastCatalog(hostDir), cloned)

	if !cloned.HasItems() {
		if err := Cleanup(diskDirectory, msUUID); err != nil {
			return nil, err
		}
		return &ProjectResult{HostDir: hostDir, MountChanged: mountChanged}, nil
	}

	if err := os.MkdirAll(hostDir, bindMountDirMode); err != nil {
		return nil, fmt.Errorf("create catalog directory: %w", err)
	}

	byName := make(map[string]ProjectItem, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		byName[name] = item
	}

	snapItems := make([]snapshotItem, 0, len(cloned.Items))
	for _, item := range cloned.Items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		info, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("missing projection source for %s", name)
		}
		snapItems = append(snapItems, snapshotItem{Name: name, Generation: info.Generation})
	}

	prev, _ := readSnapshot(hostDir)
	nextSnap := snapshot{
		BindPath:    cloned.BindPath,
		Permissions: cloned.Permissions,
		Items:       snapItems,
	}
	if snapshotContentEqual(prev, snapItems) {
		if err := syncNameLinks(hostDir, cloned.Items); err != nil {
			return nil, err
		}
		if !mountChanged {
			return &ProjectResult{HostDir: hostDir, MountChanged: false}, nil
		}
		if err := writeSnapshot(hostDir, nextSnap); err != nil {
			return nil, err
		}
		return &ProjectResult{HostDir: hostDir, MountChanged: true}, nil
	}

	versionName := newVersionDirName()
	versionDir := filepath.Join(hostDir, versionName)
	if err := os.MkdirAll(versionDir, bindMountDirMode); err != nil {
		return nil, fmt.Errorf("create catalog version directory: %w", err)
	}

	for _, item := range cloned.Items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		info := byName[name]
		content := strings.TrimSpace(info.ContentPath)
		if content == "" {
			content = modelpull.ContentDir(modelpull.Root(diskDirectory), name)
		}
		dest := filepath.Join(versionDir, name)
		if err := projectContent(content, dest); err != nil {
			_ = os.RemoveAll(versionDir)
			return nil, fmt.Errorf("project %s: %w", name, err)
		}
	}

	previousVersion := ""
	if target, err := os.Readlink(filepath.Join(hostDir, dataSymlink)); err == nil {
		previousVersion = target
	}
	if err := swingData(hostDir, versionName); err != nil {
		_ = os.RemoveAll(versionDir)
		return nil, err
	}
	if err := syncNameLinks(hostDir, cloned.Items); err != nil {
		return nil, err
	}
	if err := writeSnapshot(hostDir, nextSnap); err != nil {
		return nil, err
	}
	cleanupOldVersions(hostDir, versionName, previousVersion)
	return &ProjectResult{HostDir: hostDir, MountChanged: mountChanged}, nil
}

func snapshotToCatalog(s *snapshot) *models.ModelCatalog {
	if s == nil {
		return nil
	}
	c := &models.ModelCatalog{
		BindPath:    s.BindPath,
		Permissions: s.Permissions,
		Items:       make([]models.ModelCatalogItem, 0, len(s.Items)),
	}
	for _, item := range s.Items {
		c.Items = append(c.Items, models.ModelCatalogItem{Name: item.Name})
	}
	return c
}

func newVersionDirName() string {
	now := time.Now()
	return fmt.Sprintf("..%s.%09d", now.Format("2006_01_02_15_04_05"), now.Nanosecond()%1_000_000_000)
}

func swingData(hostDir, versionName string) error {
	dataLink := filepath.Join(hostDir, dataSymlink)
	tmp := dataLink + ".tmp"
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove temp catalog data link: %w", err)
	}
	if err := os.Symlink(versionName, tmp); err != nil {
		return fmt.Errorf("create catalog data link: %w", err)
	}
	if err := os.Rename(tmp, dataLink); err != nil {
		return fmt.Errorf("swing catalog data link: %w", err)
	}
	return nil
}

func syncNameLinks(hostDir string, items []models.ModelCatalogItem) error {
	wanted := make(map[string]struct{}, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		wanted[name] = struct{}{}
		linkPath := filepath.Join(hostDir, name)
		target := filepath.Join(dataSymlink, name)
		if err := ensureNameLink(linkPath, target); err != nil {
			return fmt.Errorf("create catalog name link %s: %w", name, err)
		}
	}

	entries, err := os.ReadDir(hostDir)
	if err != nil {
		return fmt.Errorf("read catalog directory: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "..") {
			continue
		}
		if _, ok := wanted[name]; ok {
			continue
		}
		path := filepath.Join(hostDir, name)
		info, err := os.Lstat(path)
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			_ = os.Remove(path)
		}
	}
	return nil
}

func ensureNameLink(linkPath, target string) error {
	if existing, err := os.Readlink(linkPath); err == nil && existing == target {
		return nil
	}
	if err := os.Remove(linkPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Symlink(target, linkPath)
}

func snapshotContentEqual(prev *snapshot, items []snapshotItem) bool {
	if prev == nil {
		return false
	}
	if len(prev.Items) != len(items) {
		return false
	}
	left := make(map[string]int64, len(prev.Items))
	for _, item := range prev.Items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			return false
		}
		left[name] = item.Generation
	}
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		gen, ok := left[name]
		if !ok || gen != item.Generation {
			return false
		}
		delete(left, name)
	}
	return len(left) == 0
}

func projectContent(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("stat content %s: %w", src, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, resErr := filepath.EvalSymlinks(src)
		if resErr != nil {
			return fmt.Errorf("resolve content %s: %w", src, resErr)
		}
		return projectContent(resolved, dst)
	}
	if !info.IsDir() {
		if err := os.MkdirAll(filepath.Dir(dst), bindMountDirMode); err != nil {
			return err
		}
		return linkOrCopyFile(src, dst)
	}
	if err := os.MkdirAll(dst, bindMountDirMode); err != nil {
		return err
	}
	return filepath.Walk(src, func(path string, walkInfo os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		mode := walkInfo.Mode()
		if mode&os.ModeSymlink != 0 {
			resolved, resErr := filepath.EvalSymlinks(path)
			if resErr != nil {
				return resErr
			}
			st, stErr := os.Stat(resolved)
			if stErr != nil {
				return stErr
			}
			if st.IsDir() {
				return os.MkdirAll(target, bindMountDirMode)
			}
			if err := os.MkdirAll(filepath.Dir(target), bindMountDirMode); err != nil {
				return err
			}
			return linkOrCopyFile(resolved, target)
		}
		if walkInfo.IsDir() {
			return os.MkdirAll(target, bindMountDirMode)
		}
		if err := os.MkdirAll(filepath.Dir(target), bindMountDirMode); err != nil {
			return err
		}
		return linkOrCopyFile(path, target)
	})
}

func linkOrCopyFile(src, dst string) error {
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := linkFile(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src) // #nosec G304 -- src is under the model content tree
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, bindMountFileMode) // #nosec G304,G302 -- dst is under the catalog version dir
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Chmod(bindMountFileMode) // #nosec G302 -- projected files 0644
}

func readSnapshot(hostDir string) (*snapshot, error) {
	raw, err := os.ReadFile(filepath.Join(hostDir, catalogMetaFile)) // #nosec G304 -- path is the catalog meta file
	if err != nil {
		return nil, err
	}
	var s snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func writeSnapshot(hostDir string, s snapshot) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	path := filepath.Join(hostDir, catalogMetaFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil { // #nosec G306 -- catalog meta is operator-readable
		return err
	}
	return os.Rename(tmp, path)
}

func cleanupOldVersions(hostDir string, keep ...string) {
	entries, err := os.ReadDir(hostDir)
	if err != nil {
		return
	}
	retain := make(map[string]struct{}, len(keep)+1)
	for _, name := range keep {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		retain[name] = struct{}{}
	}
	if target, err := os.Readlink(filepath.Join(hostDir, dataSymlink)); err == nil {
		retain[target] = struct{}{}
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || !strings.HasPrefix(name, "..") {
			continue
		}
		if name == dataSymlink {
			continue
		}
		if _, ok := retain[name]; ok {
			continue
		}
		_ = os.RemoveAll(filepath.Join(hostDir, name))
	}
}

func lastCatalog(hostDir string) *models.ModelCatalog {
	s, err := readSnapshot(hostDir)
	if err != nil {
		return nil
	}
	return snapshotToCatalog(s)
}

func mountChangedFromDisk(hostDir string, desired *models.ModelCatalog) bool {
	return models.CatalogMountNeedsRecreate(lastCatalog(hostDir), desired)
}
