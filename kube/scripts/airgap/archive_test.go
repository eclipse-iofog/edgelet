package airgap

import (
	"archive/tar"
	"bytes"
	"slices"
	"testing"
	"time"
)

func TestListMatchesPinList(t *testing.T) {
	var raw bytes.Buffer
	if err := WriteTar(&raw, Images()); err != nil {
		t.Fatal(err)
	}
	plain, err := List(bytes.NewReader(raw.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plain, Images()) {
		t.Fatalf("tar %v", plain)
	}

	path := t.TempDir() + "/" + AssetAMD64
	if err := Compress(path, bytes.NewReader(raw.Bytes())); err != nil {
		t.Fatal(err)
	}
	got, err := ListFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, Images()) || len(got) != 12 {
		t.Fatalf("archive %v", got)
	}
}

func TestListKeepsIndexNames(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	mod := time.Unix(0, 0)
	index := []byte(`{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":1,"annotations":{"io.containerd.image.name":"docker.io/portainer/pause:latest@sha256:df6d2fb5cff97c4070a12953a34a56422879f1e02b43400f1caf1cbd530458b1","org.opencontainers.image.ref.name":"latest"}}]}`)
	manifest := []byte(`[{"Config":"blobs/sha256/abc","RepoTags":["pause:old"],"Layers":[]}]`)
	for _, member := range []struct {
		name string
		body []byte
	}{
		{"oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`)},
		{"index.json", index},
		{"manifest.json", manifest},
	} {
		hdr := &tar.Header{Name: member.name, Mode: 0o644, Size: int64(len(member.body)), ModTime: mod, Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(member.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := List(&buf)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"docker.io/portainer/pause:latest@sha256:df6d2fb5cff97c4070a12953a34a56422879f1e02b43400f1caf1cbd530458b1"}
	if !slices.Equal(got, want) {
		t.Fatalf("names %v", got)
	}
}

func TestListReadsDockerRepoTags(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := []byte(`[{"Config":"blobs/sha256/abc","RepoTags":["example.com/one:1","example.com/two:2"],"Layers":[]}]`)
	hdr := &tar.Header{Name: "manifest.json", Mode: 0o644, Size: int64(len(body)), ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := List(&buf)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"example.com/one:1", "example.com/two:2"}
	if !slices.Equal(got, want) {
		t.Fatalf("names %v", got)
	}
}
