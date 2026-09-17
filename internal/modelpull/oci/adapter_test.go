package oci

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/modelpull"
	"github.com/eclipse-iofog/edgelet/internal/modelpull/format"
	"github.com/eclipse-iofog/edgelet/internal/modelpull/ocistore"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type registryFixture struct {
	tag          string
	manifest     []byte
	manifestDesc ocispec.Descriptor
	blobs        map[string][]byte
	failAfter    map[string]int
	mu           sync.Mutex
}

func dockerModelFixture(t *testing.T, layer []byte) *registryFixture {
	t.Helper()
	config := []byte(`{"config":{"format":"gguf","size":"32"},"files":[]}`)
	configDigest := digest.FromBytes(config)
	layerDigest := digest.FromBytes(layer)
	manifest := ocispec.Manifest{
		MediaType: ocispec.MediaTypeImageManifest,
		Config: ocispec.Descriptor{
			MediaType: format.MediaDockerModelConfigV01,
			Digest:    configDigest,
			Size:      int64(len(config)),
		},
		Layers: []ocispec.Descriptor{{
			MediaType: format.MediaDockerGGUF,
			Digest:    layerDigest,
			Size:      int64(len(layer)),
		}},
	}
	manifest.SchemaVersion = 2
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	manifestDigest := digest.FromBytes(raw)
	return &registryFixture{
		tag:      "4b-q8_0",
		manifest: raw,
		manifestDesc: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageManifest,
			Digest:    manifestDigest,
			Size:      int64(len(raw)),
		},
		blobs: map[string][]byte{
			string(configDigest): config,
			string(layerDigest):  layer,
		},
		failAfter: map[string]int{},
	}
}

func (f *registryFixture) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" || r.URL.Path == "/v2" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if strings.Contains(r.URL.Path, "/manifests/") {
			f.serveManifest(w, r)
			return
		}
		if strings.Contains(r.URL.Path, "/blobs/") {
			f.serveBlob(w, r)
			return
		}
		http.NotFound(w, r)
	})
}

func (f *registryFixture) serveManifest(w http.ResponseWriter, r *http.Request) {
	ref := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	if ref != f.tag && ref != string(f.manifestDesc.Digest) && ref != "latest" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
	w.Header().Set("Docker-Content-Digest", string(f.manifestDesc.Digest))
	w.Header().Set("Content-Length", strconv.Itoa(len(f.manifest)))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(f.manifest)
}

func (f *registryFixture) serveBlob(w http.ResponseWriter, r *http.Request) {
	digestRef := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	f.mu.Lock()
	payload, ok := f.blobs[digestRef]
	failAfter := f.failAfter[digestRef]
	f.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}

	start := int64(0)
	if rng := r.Header.Get("Range"); strings.HasPrefix(rng, "bytes=") {
		spec := strings.TrimPrefix(rng, "bytes=")
		spec = strings.TrimSuffix(spec, "-")
		if n, err := strconv.ParseInt(spec, 10, 64); err == nil {
			start = n
		}
	}

	if start > 0 {
		if start > int64(len(payload)) {
			http.Error(w, "invalid range", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		body := payload[start:]
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Range", "bytes "+strconv.FormatInt(start, 10)+"-"+strconv.Itoa(len(payload)-1)+"/"+strconv.Itoa(len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusPartialContent)
		if r.Method != http.MethodHead {
			_, _ = w.Write(body)
		}
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Docker-Content-Digest", digestRef)
	if r.Method != http.MethodHead && failAfter > 0 && failAfter < len(payload) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload[:failAfter])
		f.mu.Lock()
		delete(f.failAfter, digestRef)
		f.mu.Unlock()
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(payload)
}

func startFixtureRegistry(t *testing.T, fixture *registryFixture) (*httptest.Server, *models.Registry) {
	t.Helper()
	srv := httptest.NewServer(fixture.handler())
	t.Cleanup(srv.Close)
	reg := models.NewRegistryBuilder().
		SetID(1).
		SetURL(srv.URL).
		SetType(models.RegistryTypeOCI).
		SetIsPublic(true).
		SetInsecure(true).
		Build()
	return srv, reg
}

func TestAdapterPull_DockerModelSpec(t *testing.T) {
	layer := []byte("GGUF-fake-weights-for-edgelet")
	fixture := dockerModelFixture(t, layer)
	_, reg := startFixtureRegistry(t, fixture)

	root := t.TempDir()
	adapter := &Adapter{}
	result, err := adapter.Pull(context.Background(), modelpull.Request{
		Name:       "gemma3",
		Repo:       "ai/gemma3",
		Revision:   "4b-q8_0",
		Registry:   reg,
		ModelsRoot: root,
		FormatHint: models.ModelFormatGGUF,
	})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if result.Digest != string(fixture.manifestDesc.Digest) {
		t.Fatalf("digest: got %q want %q", result.Digest, fixture.manifestDesc.Digest)
	}
	if result.RevisionKind != modelpull.RevisionKindTag || !result.RevisionFloating {
		t.Fatalf("revision: %+v", result)
	}
	if result.Format != models.ModelFormatGGUF {
		t.Fatalf("format: got %q", result.Format)
	}

	contentFile := filepath.Join(root, "gemma3", "content", "model.gguf")
	got, err := os.ReadFile(contentFile)
	if err != nil {
		t.Fatalf("read content: %v", err)
	}
	if !bytes.Equal(got, layer) {
		t.Fatal("materialized weights mismatch")
	}

	store, err := ocistore.Open(filepath.Join(root, "oci-store"))
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	layout, err := store.ReadLayout()
	if err != nil || layout.Version != "1.0.0" {
		t.Fatalf("layout: %+v err=%v", layout, err)
	}
	idx, err := store.ReadIndex()
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if len(idx.Models) != 1 || idx.Models[0].ID != result.Digest {
		t.Fatalf("index entry: %+v", idx.Models)
	}
	if len(idx.Models[0].Tags) != 1 || idx.Models[0].Tags[0] != "ai/gemma3:4b-q8_0" {
		t.Fatalf("index tags: %v", idx.Models[0].Tags)
	}

	onDiskRaw, err := os.ReadFile(filepath.Join(root, "gemma3", "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest.json: %v", err)
	}
	var onDisk modelpull.OnDiskManifest
	if err := json.Unmarshal(onDiskRaw, &onDisk); err != nil {
		t.Fatalf("decode manifest.json: %v", err)
	}
	if onDisk.Digest != result.Digest || onDisk.RegistryType != models.RegistryTypeOCI {
		t.Fatalf("on-disk manifest: %+v", onDisk)
	}
	if len(onDisk.ContentPaths) == 0 || onDisk.OCIModelConfig == nil {
		t.Fatalf("expected content paths and oci model config, got %+v", onDisk)
	}
}

func TestAdapterPull_ResumeInterruptedBlob(t *testing.T) {
	layer := bytes.Repeat([]byte("W"), 32*1024)
	fixture := dockerModelFixture(t, layer)
	layerDigest := string(digest.FromBytes(layer))
	fixture.failAfter[layerDigest] = 4096
	_, reg := startFixtureRegistry(t, fixture)

	root := t.TempDir()
	req := modelpull.Request{
		Name:       "gemma3",
		Repo:       "ai/gemma3",
		Revision:   "4b-q8_0",
		Registry:   reg,
		ModelsRoot: root,
	}
	adapter := &Adapter{}
	_, err := adapter.Pull(context.Background(), req)
	if err == nil {
		t.Fatal("expected first pull to fail on interrupted blob")
	}

	store, err := ocistore.Open(filepath.Join(root, "oci-store"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	n, err := store.IncompleteSize(layerDigest)
	if err != nil {
		t.Fatalf("incomplete size: %v", err)
	}
	if n == 0 {
		t.Fatal("expected incomplete blob to be retained")
	}
	if _, err := os.Stat(filepath.Join(root, "gemma3", "manifest.json")); !os.IsNotExist(err) {
		t.Fatal("manifest.json must not be written for a failed pull")
	}

	result, err := adapter.Pull(context.Background(), req)
	if err != nil {
		t.Fatalf("resume pull: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "gemma3", "content", "model.gguf"))
	if err != nil {
		t.Fatalf("read resumed content: %v", err)
	}
	if !bytes.Equal(got, layer) {
		t.Fatalf("resumed content mismatch (len=%d)", len(got))
	}
	has, err := store.HasBlob(layerDigest)
	if err != nil || !has {
		t.Fatalf("expected complete blob after resume, has=%v err=%v", has, err)
	}
	if result.Digest != string(fixture.manifestDesc.Digest) {
		t.Fatalf("digest after resume: %q", result.Digest)
	}
}

func TestAdapterPull_RejectsNonOCIRegistry(t *testing.T) {
	adapter := &Adapter{}
	hf := models.NewRegistryBuilder().SetID(5).SetURL("https://huggingface.co").SetType(models.RegistryTypeHF).Build()
	_, err := adapter.Pull(context.Background(), modelpull.Request{
		Name:       "x",
		Repo:       "org/repo",
		Registry:   hf,
		ModelsRoot: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "cannot pull OCI") {
		t.Fatalf("expected hf registry rejection, got %v", err)
	}
}

func TestAdapterPull_IgnoresSpecFiles(t *testing.T) {
	layer := []byte("full-artifact")
	fixture := dockerModelFixture(t, layer)
	_, reg := startFixtureRegistry(t, fixture)
	root := t.TempDir()
	_, err := (&Adapter{}).Pull(context.Background(), modelpull.Request{
		Name:       "gemma3",
		Repo:       "ai/gemma3",
		Revision:   "",
		Registry:   reg,
		ModelsRoot: root,
	})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	// empty revision → latest; fixture serves "latest" as the same manifest
	if _, err := os.Stat(filepath.Join(root, "gemma3", "content", "model.gguf")); err != nil {
		t.Fatalf("expected full artifact in content/: %v", err)
	}
}
