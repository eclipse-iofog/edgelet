package knowledgecatalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

func TestProject_HardlinkLayoutAndDataSwing(t *testing.T) {
	disk := t.TempDir()
	content := filepath.Join(disk, "knowledge", "product-docs", "content")
	if err := os.MkdirAll(content, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(content, "guide.md")
	if err := os.WriteFile(src, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	catalog := &models.KnowledgeCatalog{
		BindPath:    "/knowledge",
		Permissions: models.KnowledgeCatalogPermRO,
		Items:       []models.KnowledgeCatalogItem{{Name: "product-docs"}},
	}
	first, err := Project(disk, "ms-1", catalog, []ProjectItem{{
		Name: "product-docs", ContentPath: content, Generation: 1,
	}})
	if err != nil {
		t.Fatalf("project: %v", err)
	}

	projected := filepath.Join(first.HostDir, "product-docs", "guide.md")
	got, err := os.ReadFile(projected) // #nosec G304 -- test fixture
	if err != nil {
		t.Fatalf("read projected: %v", err)
	}
	if string(got) != "v1" {
		t.Fatalf("expected v1, got %q", got)
	}

	srcInfo, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	dstInfo, err := os.Stat(projected)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(srcInfo, dstInfo) {
		t.Fatal("expected hardlink (same file) under {name}/")
	}

	link, err := os.Readlink(filepath.Join(first.HostDir, "product-docs"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.IsAbs(link) {
		t.Fatalf("name link must be relative, got %q", link)
	}

	dirBefore, err := os.Stat(first.HostDir)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(src, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := Project(disk, "ms-1", catalog, []ProjectItem{{
		Name: "product-docs", ContentPath: content, Generation: 2,
	}})
	if err != nil {
		t.Fatalf("project after generation bump: %v", err)
	}
	dirAfter, err := os.Stat(second.HostDir)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(dirBefore, dirAfter) {
		t.Fatal("catalog directory inode must stay the same across ..data swing")
	}
	got, err = os.ReadFile(filepath.Join(second.HostDir, "product-docs", "guide.md")) // #nosec G304 -- test fixture
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v2" {
		t.Fatalf("expected swung content v2, got %q", got)
	}

	versions := catalogVersionDirs(t, second.HostDir)
	if len(versions) < 2 {
		t.Fatalf("expected previous version directory to be kept, got %v", versions)
	}

	linkPath := filepath.Join(second.HostDir, "product-docs")
	beforeLink, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	third, err := Project(disk, "ms-1", catalog, []ProjectItem{{
		Name: "product-docs", ContentPath: content, Generation: 2,
	}})
	if err != nil {
		t.Fatalf("unchanged project: %v", err)
	}
	afterLink, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(beforeLink, afterLink) {
		t.Fatal("unchanged projection must keep the existing name symlink")
	}
	afterVersions := catalogVersionDirs(t, third.HostDir)
	if len(afterVersions) != len(versions) {
		t.Fatalf("unchanged projection must not add a version directory: before %v after %v", versions, afterVersions)
	}
}

func catalogVersionDirs(t *testing.T, hostDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(hostDir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || !strings.HasPrefix(name, "..") || name == dataSymlink {
			continue
		}
		names = append(names, name)
	}
	return names
}

func TestProject_BindPathChangeSetsMountChanged(t *testing.T) {
	disk := t.TempDir()
	content := filepath.Join(disk, "knowledge", "product-docs", "content")
	if err := os.MkdirAll(content, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(content, "guide.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	item := []ProjectItem{{Name: "product-docs", ContentPath: content, Generation: 1}}
	first := &models.KnowledgeCatalog{BindPath: "/knowledge", Items: []models.KnowledgeCatalogItem{{Name: "product-docs"}}}
	if _, err := Project(disk, "ms-bind", first, item); err != nil {
		t.Fatal(err)
	}
	sameBind := first.Clone()
	res, err := Project(disk, "ms-bind", sameBind, item)
	if err != nil {
		t.Fatal(err)
	}
	if res.MountChanged {
		t.Fatal("same bindPath must not report mount change")
	}
	moved := first.Clone()
	moved.BindPath = "/corpus"
	res, err = Project(disk, "ms-bind", moved, item)
	if err != nil {
		t.Fatal(err)
	}
	if !res.MountChanged {
		t.Fatal("bindPath change must report mount change")
	}
}
