package images

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet-kube/scripts/airgap"
)

func TestPinList(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "scripts", "airgap", "image-list.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		want = append(want, line)
	}
	got := airgap.Images()
	if !slices.Equal(got, want) {
		t.Fatalf("list %v", got)
	}
	if len(got) != 12 {
		t.Fatalf("got %d images", len(got))
	}
	if !strings.HasPrefix(got[0], "docker.io/portainer/pause:latest@sha256:df6d2fb5") {
		t.Fatal(got[0])
	}
}

func TestEmptyDirectoryPullsAndPins(t *testing.T) {
	store := newMemStore()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	dir := t.TempDir()
	if err := run(ctx, dir, time.Second, io.Discard, store, ""); err != nil {
		t.Fatal(err)
	}
	want := airgap.ImagesFor(runtime.GOARCH)
	if !slices.Equal(store.pulled(), want) {
		t.Fatalf("pulled %v", store.pulled())
	}
	for _, ref := range want {
		if !store.isPinned(ref) {
			t.Fatalf("%s is not pinned", ref)
		}
	}
	if len(store.imported()) != 0 {
		t.Fatalf("imported %v", store.imported())
	}
}

func TestDirectoryWithFileDoesNotPull(t *testing.T) {
	store := newMemStore()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, dir, time.Second, io.Discard, store, ""); err != nil {
		t.Fatal(err)
	}
	if len(store.pulled()) != 0 {
		t.Fatalf("pulled %v", store.pulled())
	}
}

func TestArchiveIsImportedAndPinned(t *testing.T) {
	store := newMemStore()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	dir := t.TempDir()
	const ref = "example.com/already:1"
	if err := os.WriteFile(filepath.Join(dir, "already.tar"), []byte(ref), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, dir, time.Second, io.Discard, store, ""); err != nil {
		t.Fatal(err)
	}
	if len(store.pulled()) != 0 {
		t.Fatalf("pulled %v", store.pulled())
	}
	if !store.isPinned(ref) {
		t.Fatalf("pinned %#v", store.pinnedCopy())
	}
}

func TestTarAddedAfterStartIsImported(t *testing.T) {
	store := newMemStore()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, dir, 50*time.Millisecond, io.Discard, store, ""); err != nil {
		t.Fatal(err)
	}
	const ref = "example.com/added:1"
	if err := os.WriteFile(filepath.Join(dir, "added.tar"), []byte(ref), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !store.isPinned(ref) {
		if time.Now().After(deadline) {
			t.Fatalf("image was not imported, pinned %#v", store.pinnedCopy())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(store.pulled()) != 0 {
		t.Fatalf("pulled %v", store.pulled())
	}
}

func TestOpenGzipArchive(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "layer.tar.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	reader, err := openArchive(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = reader.Close()
	}()
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "hello" {
		t.Fatalf("body %q", body)
	}
}

func TestPrefixRetagsPinList(t *testing.T) {
	store := newMemStore()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	const registry = "registry.example"
	if err := run(ctx, t.TempDir(), time.Second, io.Discard, store, registry); err != nil {
		t.Fatal(err)
	}
	pulled := airgap.ImagesFor(runtime.GOARCH)
	if !slices.Equal(store.pulled(), pulled) {
		t.Fatalf("pulled %v", store.pulled())
	}
	if len(airgap.Images()) != 12 {
		t.Fatalf("got %d images", len(airgap.Images()))
	}
	for _, ref := range pulled {
		for _, name := range airgap.PrefixedNames(registry, ref) {
			if !store.isPinned(name) {
				t.Fatalf("%s is not pinned", name)
			}
		}
		if !strings.HasPrefix(airgap.Resolve(registry, ref), registry+"/") {
			t.Fatalf("resolve %s", airgap.Resolve(registry, ref))
		}
	}
	pauseName, _, _ := strings.Cut(airgap.Images()[0], "@")
	pause := airgap.Resolve(registry, pauseName)
	if pause != registry+"/"+pauseName || !store.isPinned(pause) {
		t.Fatalf("pause %s pinned %v", pause, store.isPinned(pause))
	}
}

func TestEmptyRegistryLeavesPinList(t *testing.T) {
	for _, registry := range []string{"", "  ", "/"} {
		if !slices.Equal(airgap.Names(registry), airgap.Images()) {
			t.Fatalf("registry %q changed the pin list", registry)
		}
	}
	store := newMemStore()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	if err := run(ctx, t.TempDir(), time.Second, io.Discard, store, ""); err != nil {
		t.Fatal(err)
	}
	want := airgap.ImagesFor(runtime.GOARCH)
	if !slices.Equal(store.pulled(), want) {
		t.Fatalf("pulled %v", store.pulled())
	}
	for _, ref := range want {
		if !store.isPinned(ref) {
			t.Fatalf("%s is not pinned", ref)
		}
	}
}

func TestOperatorImageIsNotRewritten(t *testing.T) {
	const registry = "registry.example"
	const operator = "example.com/operator-app:9"
	if airgap.Resolve(registry, operator) != operator {
		t.Fatalf("resolve %s", airgap.Resolve(registry, operator))
	}
	if len(airgap.PrefixedNames(registry, operator)) != 0 {
		t.Fatal("operator image was prefixed")
	}
	store := newMemStore()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "operator.tar"), []byte(operator), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, dir, time.Second, io.Discard, store, registry); err != nil {
		t.Fatal(err)
	}
	if !store.isPinned(operator) {
		t.Fatal("operator image was not pinned")
	}
	for name := range store.pinnedCopy() {
		if strings.Contains(name, registry) {
			t.Fatalf("rewrote %s", name)
		}
	}
	if len(store.pulled()) != 0 {
		t.Fatalf("pulled %v", store.pulled())
	}
}

func TestImportRetagsListedImage(t *testing.T) {
	store := newMemStore()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	dir := t.TempDir()
	ref := airgap.Images()[0]
	if err := os.WriteFile(filepath.Join(dir, "pause.tar"), []byte(ref), 0o644); err != nil {
		t.Fatal(err)
	}
	const registry = "registry.example"
	if err := run(ctx, dir, time.Second, io.Discard, store, registry); err != nil {
		t.Fatal(err)
	}
	if len(store.pulled()) != 0 {
		t.Fatalf("pulled %v", store.pulled())
	}
	for _, name := range airgap.PrefixedNames(registry, ref) {
		if !store.isPinned(name) {
			t.Fatalf("%s is not pinned", name)
		}
	}
	if !store.isPinned(ref) {
		t.Fatal("imported name was not pinned")
	}
}

func TestEmptyDirectoryAndArchivePinTheSameNames(t *testing.T) {
	if Namespace != "k8s.io" {
		t.Fatal(Namespace)
	}
	empty := pinnedNames(t, t.TempDir(), "")
	dir := t.TempDir()
	writeAirgapArchive(t, dir)
	seeded := pinnedNames(t, dir, "")
	if !slices.Equal(empty, seeded) {
		t.Fatalf("empty %v archive %v", empty, seeded)
	}
	want := slices.Clone(airgap.ImagesFor(runtime.GOARCH))
	slices.Sort(want)
	if !slices.Equal(empty, want) {
		t.Fatalf("pinned %v", empty)
	}
}

func TestRegistryPauseNameIsPinnedOnBothPaths(t *testing.T) {
	const registry = "registry.example"
	pauseName, _, _ := strings.Cut(airgap.Images()[0], "@")
	pause := airgap.Resolve(registry, pauseName)
	if pause != registry+"/"+pauseName {
		t.Fatalf("pause %s", pause)
	}
	empty := pinnedNames(t, t.TempDir(), registry)
	dir := t.TempDir()
	writeAirgapArchive(t, dir)
	seeded := pinnedNames(t, dir, registry)
	if !slices.Contains(empty, pause) || !slices.Contains(seeded, pause) {
		t.Fatalf("empty %v archive %v", empty, seeded)
	}
	if !slices.Equal(empty, seeded) {
		t.Fatalf("empty %v archive %v", empty, seeded)
	}
}

func pinnedNames(t *testing.T, dir, registry string) []string {
	t.Helper()
	store := newMemStore()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	if err := run(ctx, dir, time.Second, io.Discard, store, registry); err != nil {
		t.Fatal(err)
	}
	names := store.pinnedNames()
	if len(store.imported()) > 0 && len(store.pulled()) > 0 {
		t.Fatalf("pulled %v and imported %v", store.pulled(), store.imported())
	}
	return names
}

func writeAirgapArchive(t *testing.T, dir string) {
	t.Helper()
	var raw bytes.Buffer
	if err := airgap.WriteTar(&raw, airgap.ImagesFor(runtime.GOARCH)); err != nil {
		t.Fatal(err)
	}
	if err := airgap.Compress(filepath.Join(dir, airgap.AssetAMD64), bytes.NewReader(raw.Bytes())); err != nil {
		t.Fatal(err)
	}
}

func TestAirgapArchiveImportsPinList(t *testing.T) {
	if Namespace != "k8s.io" {
		t.Fatal(Namespace)
	}
	dir := t.TempDir()
	archive := filepath.Join(dir, airgap.AssetAMD64)
	var raw bytes.Buffer
	if err := airgap.WriteTar(&raw, airgap.Images()); err != nil {
		t.Fatal(err)
	}
	if err := airgap.Compress(archive, bytes.NewReader(raw.Bytes())); err != nil {
		t.Fatal(err)
	}
	names, err := airgap.ListFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(names, airgap.Images()) {
		t.Fatalf("names %v", names)
	}
	if len(names) != 12 {
		t.Fatalf("got %d images", len(names))
	}
	store := newMemStore()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	if err := run(ctx, dir, time.Second, io.Discard, store, ""); err != nil {
		t.Fatal(err)
	}
	if len(store.pulled()) != 0 {
		t.Fatalf("pulled %v", store.pulled())
	}
	if !slices.Equal(store.importedRefs(), airgap.Images()) {
		t.Fatalf("imported %v", store.importedRefs())
	}
	if len(store.pinnedCopy()) != len(airgap.Images()) {
		t.Fatalf("pinned %#v", store.pinnedCopy())
	}
	for _, ref := range airgap.Images() {
		if !store.isPinned(ref) {
			t.Fatalf("%s is not pinned", ref)
		}
	}
}

func TestArchiveNames(t *testing.T) {
	for _, name := range []string{"a.tar", "a.TAR", "a.tar.gz", "a.tgz", "a.tar.zst"} {
		if !isArchive(name) {
			t.Fatalf("%s should be an archive", name)
		}
	}
	for _, name := range []string{"a.txt", "a.tar.gz.txt", "image-list.txt"} {
		if isArchive(name) {
			t.Fatalf("%s should not be an archive", name)
		}
	}
}

type memStore struct {
	mu         sync.Mutex
	pulls      []string
	imports    []string
	importRefs []string
	present    map[string]bool
	pinned     map[string]bool
}

func newMemStore() *memStore {
	return &memStore{
		present: map[string]bool{},
		pinned:  map[string]bool{},
	}
}

func (m *memStore) Pull(_ context.Context, ref string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pulls = append(m.pulls, ref)
	m.present[ref] = true
	return nil
}

func (m *memStore) Import(_ context.Context, path string) ([]string, error) {
	names, err := airgap.ListFile(path)
	if err != nil {
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, readErr
		}
		names = []string{strings.TrimSpace(string(body))}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.imports = append(m.imports, path)
	m.importRefs = append(m.importRefs, names...)
	for _, name := range names {
		m.present[name] = true
	}
	return names, nil
}

func (m *memStore) Retag(_ context.Context, from, to string) error {
	if from == to {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.present[from] {
		return os.ErrNotExist
	}
	m.present[to] = true
	return nil
}

func (m *memStore) Pin(_ context.Context, names []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, name := range names {
		if !m.present[name] {
			return os.ErrNotExist
		}
		m.pinned[name] = true
	}
	return nil
}

func (m *memStore) Close() error {
	return nil
}

func (m *memStore) pulled() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.pulls)
}

func (m *memStore) imported() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.imports)
}

func (m *memStore) importedRefs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.importRefs)
}

func (m *memStore) isPinned(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pinned[name]
}

func (m *memStore) pinnedNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.pinned))
	for name, pinned := range m.pinned {
		if pinned {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func (m *memStore) pinnedCopy() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]bool, len(m.pinned))
	for name, pinned := range m.pinned {
		out[name] = pinned
	}
	return out
}
