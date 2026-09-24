package format

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type mapBlobs map[string][]byte

func (m mapBlobs) Open(d string) (io.ReadCloser, error) {
	raw, ok := m[d]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(raw)), nil
}

func TestUnpack_DockerGGUF(t *testing.T) {
	weights := []byte("fake-gguf-weights")
	license := []byte("MIT")
	wDigest := digest.FromBytes(weights)
	lDigest := digest.FromBytes(license)
	manifest := ocispec.Manifest{
		Config: ocispec.Descriptor{MediaType: MediaDockerModelConfigV01},
		Layers: []ocispec.Descriptor{
			{MediaType: MediaDockerGGUF, Digest: wDigest, Size: int64(len(weights))},
			{MediaType: MediaDockerLicense, Digest: lDigest, Size: int64(len(license))},
		},
	}
	blobs := mapBlobs{
		string(wDigest): weights,
		string(lDigest): license,
	}
	dir := t.TempDir()
	got, err := Unpack(KindDocker, manifest, blobs, dir)
	if err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if got.Format != "gguf" {
		t.Fatalf("format: got %q", got.Format)
	}
	modelPath := filepath.Join(dir, "model.gguf")
	raw, err := os.ReadFile(modelPath)
	if err != nil {
		t.Fatalf("read model.gguf: %v", err)
	}
	if !bytes.Equal(raw, weights) {
		t.Fatal("model.gguf mismatch")
	}
	if _, err := os.Stat(filepath.Join(dir, "LICENSE")); err != nil {
		t.Fatalf("LICENSE missing: %v", err)
	}
}

func TestUnpack_ModelPackFilepath(t *testing.T) {
	payload := []byte("safetensors-bytes")
	d := digest.FromBytes(payload)
	manifest := ocispec.Manifest{
		ArtifactType: MediaModelPackManifest,
		Layers: []ocispec.Descriptor{{
			MediaType:   "application/vnd.cncf.model.weight.v1.raw",
			Digest:      d,
			Size:        int64(len(payload)),
			Annotations: map[string]string{AnnotationFilePath: "weights/model.safetensors"},
		}},
	}
	dir := t.TempDir()
	got, err := Unpack(KindModelPack, manifest, mapBlobs{string(d): payload}, dir)
	if err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if len(got.RelPaths) != 1 || got.RelPaths[0] != "content/weights/model.safetensors" {
		t.Fatalf("content paths: %v", got.RelPaths)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "weights", "model.safetensors"))
	if err != nil {
		t.Fatalf("read unpacked file: %v", err)
	}
	if !bytes.Equal(raw, payload) {
		t.Fatal("payload mismatch")
	}
}

func TestExtractTar_WritesRegularFile(t *testing.T) {
	payload := []byte("weights")
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "model.bin", Size: int64(len(payload)), Mode: 0o644}); err != nil {
		t.Fatalf("header: %v", err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	dir := t.TempDir()
	paths, err := extractTar(&buf, dir, false)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(paths) != 1 || paths[0] != "model.bin" {
		t.Fatalf("paths: %v", paths)
	}
	got, err := os.ReadFile(filepath.Join(dir, "model.bin"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("extracted bytes mismatch")
	}
}

func TestExtractTar_RejectsOversizedEntry(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "huge.bin", Size: maxTarEntryBytes + 1, Mode: 0o644}); err != nil {
		t.Fatalf("header: %v", err)
	}
	dir := t.TempDir()
	_, err := extractTar(bytes.NewReader(buf.Bytes()), dir, false)
	if err == nil || !strings.Contains(err.Error(), "invalid size") {
		t.Fatalf("expected invalid size, got %v", err)
	}
}

func TestUnpack_RejectsPathEscape(t *testing.T) {
	payload := []byte("x")
	d := digest.FromBytes(payload)
	manifest := ocispec.Manifest{
		Layers: []ocispec.Descriptor{{
			MediaType:   MediaTypeOctetStream,
			Digest:      d,
			Annotations: map[string]string{AnnotationFilePath: "../outside.bin"},
		}},
	}
	_, err := Unpack(KindORAS, manifest, mapBlobs{string(d): payload}, t.TempDir())
	if err == nil {
		t.Fatal("expected path escape to fail")
	}
}
