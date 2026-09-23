package untar

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestExtractRegularFileWritesOnlyRequestedMember(t *testing.T) {
	t.Parallel()

	payload := []byte("\x7fELFfat-runtime")
	blob := zstdTar(t, map[string][]byte{
		"bin/other":            []byte("skip-me"),
		"images/pause.tar.gz":  []byte("pause"),
		"bin/edgelet":          payload,
		"bin/edgelet-not-this": []byte("nope"),
	})

	dir := t.TempDir()
	dest := filepath.Join(dir, "edgelet")
	if err := ExtractRegularFile(bytes.NewReader(blob), "bin/edgelet", dest); err != nil {
		t.Fatalf("ExtractRegularFile: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("dest bytes = %q", got)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat dest: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("dest mode %v is not executable", info.Mode())
	}
	for _, name := range []string{"other", "pause.tar.gz", "edgelet-not-this", "bin"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("unexpected extracted path %s: %v", name, err)
		}
	}
}

func TestExtractRegularFileMissingMember(t *testing.T) {
	t.Parallel()

	blob := zstdTar(t, map[string][]byte{"bin/other": []byte("x")})
	err := ExtractRegularFile(bytes.NewReader(blob), "bin/edgelet", filepath.Join(t.TempDir(), "edgelet"))
	if err == nil {
		t.Fatal("expected missing member error")
	}
}

func zstdTar(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	tw := tar.NewWriter(zw)
	for name, body := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(body)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("header %s: %v", name, err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zstd close: %v", err)
	}
	return buf.Bytes()
}
