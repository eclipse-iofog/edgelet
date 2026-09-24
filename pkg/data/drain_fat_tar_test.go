//go:build linux && !cgo

package data

import (
	"archive/tar"
	"bytes"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func mustZstdTar(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	tw := tar.NewWriter(zw)
	for name, body := range files {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))}
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
