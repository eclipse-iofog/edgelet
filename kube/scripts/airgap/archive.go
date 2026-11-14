package airgap

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

const (
	// AssetAMD64 is the airgap archive published beside the amd64 binary.
	AssetAMD64 = "edgelet-kube-airgap-images-amd64.tar.zst"

	// imageNameAnnotation is the image name recorded on an exported OCI index.
	imageNameAnnotation = "io.containerd.image.name"

	mediaTypeIndex    = "application/vnd.oci.image.index.v1+json"
	mediaTypeManifest = "application/vnd.oci.image.manifest.v1+json"
	layoutVersion     = "1.0.0"
	jsonLimit         = 20 << 20
)

// WriteTar writes an OCI image archive whose index lists refs, in order.
// A containerd export records the same names on its index.
func WriteTar(w io.Writer, refs []string) error {
	if len(refs) == 0 {
		return errors.New("archive has no images")
	}
	index, err := indexBytes(refs)
	if err != nil {
		return err
	}
	layout, err := json.Marshal(map[string]string{"imageLayoutVersion": layoutVersion})
	if err != nil {
		return fmt.Errorf("encode layout: %w", err)
	}
	tw := tar.NewWriter(w)
	mod := time.Unix(0, 0)
	if err := writeMember(tw, "oci-layout", layout, mod); err != nil {
		return err
	}
	if err := writeMember(tw, "index.json", index, mod); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("close archive: %w", err)
	}
	return nil
}

// Compress writes src to dst as a zstd archive.
// dst is replaced only after the frame is complete.
func Compress(dst string, src io.Reader) error {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create archive directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".airgap-*.partial")
	if err != nil {
		return fmt.Errorf("create archive: %w", err)
	}
	tmpName := tmp.Name()
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpName)
		}
	}()
	zw, err := zstd.NewWriter(tmp,
		zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(16)),
		zstd.WithWindowSize(1<<25),
	)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("zstd writer: %w", err)
	}
	_, copyErr := io.Copy(zw, src)
	closeErr := zw.Close()
	fileErr := tmp.Close()
	if copyErr != nil {
		return fmt.Errorf("compress archive: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("compress archive: %w", closeErr)
	}
	if fileErr != nil {
		return fmt.Errorf("compress archive: %w", fileErr)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod archive: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("write archive: %w", err)
	}
	success = true
	return nil
}

// ListFile returns the image names stored in an OCI or Docker archive.
// The archive may be a tar, gzip tar, or zstd tar. Names come from the
// OCI index when it records them, so a digested pin-list reference is kept.
func ListFile(file string) ([]string, error) {
	reader, err := openArchive(file)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = reader.Close()
	}()
	names, err := List(reader)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", file, err)
	}
	return names, nil
}

// List reads image names from an uncompressed OCI or Docker tar.
func List(r io.Reader) ([]string, error) {
	tr := tar.NewReader(r)
	var indexNames []string
	var repoTags []string
	sawIndex := false
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		switch path.Clean(hdr.Name) {
		case "index.json":
			sawIndex = true
			indexNames, err = namesFromIndex(tr)
		case "manifest.json":
			repoTags, err = namesFromDocker(tr)
		default:
			_, err = io.Copy(io.Discard, tr)
		}
		if err != nil {
			return nil, err
		}
	}
	if sawIndex && len(indexNames) > 0 {
		return indexNames, nil
	}
	if len(repoTags) > 0 {
		return repoTags, nil
	}
	return nil, errors.New("archive contains no images")
}

func indexBytes(refs []string) ([]byte, error) {
	manifests := make([]map[string]any, 0, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return nil, errors.New("archive image name is empty")
		}
		sum := sha256.Sum256([]byte(ref))
		manifests = append(manifests, map[string]any{
			"mediaType": mediaTypeManifest,
			"digest":    "sha256:" + hex.EncodeToString(sum[:]),
			"size":      0,
			"annotations": map[string]string{
				imageNameAnnotation: ref,
			},
		})
	}
	body, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     mediaTypeIndex,
		"manifests":     manifests,
	})
	if err != nil {
		return nil, fmt.Errorf("encode index: %w", err)
	}
	return body, nil
}

func writeMember(tw *tar.Writer, name string, body []byte, mod time.Time) error {
	hdr := &tar.Header{
		Name:     name,
		Mode:     0o644,
		Size:     int64(len(body)),
		ModTime:  mod,
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	if _, err := tw.Write(body); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

type indexManifest struct {
	Annotations map[string]string `json:"annotations"`
}

func namesFromIndex(r io.Reader) ([]string, error) {
	var index struct {
		Manifests []indexManifest `json:"manifests"`
	}
	if err := decodeJSON(r, &index); err != nil {
		return nil, fmt.Errorf("read index: %w", err)
	}
	var names []string
	seen := map[string]struct{}{}
	for _, manifest := range index.Manifests {
		name := strings.TrimSpace(manifest.Annotations[imageNameAnnotation])
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names, nil
}

func namesFromDocker(r io.Reader) ([]string, error) {
	var manifests []struct {
		RepoTags []string `json:"RepoTags"`
	}
	if err := decodeJSON(r, &manifests); err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var names []string
	seen := map[string]struct{}{}
	for _, manifest := range manifests {
		for _, name := range manifest.RepoTags {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			names = append(names, name)
		}
	}
	return names, nil
}

func decodeJSON(r io.Reader, dest any) error {
	body, err := io.ReadAll(io.LimitReader(r, jsonLimit+1))
	if err != nil {
		return err
	}
	if len(body) > jsonLimit {
		_, _ = io.Copy(io.Discard, r)
		return fmt.Errorf("json exceeds %d bytes", jsonLimit)
	}
	if len(body) == 0 {
		return errors.New("json is empty")
	}
	return json.Unmarshal(body, dest)
}

func openArchive(file string) (io.ReadCloser, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	var magic [4]byte
	n, err := io.ReadFull(f, magic[:])
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		_ = f.Close()
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, err
	}
	switch {
	case n >= 4 && magic[0] == 0x28 && magic[1] == 0xB5 && magic[2] == 0x2F && magic[3] == 0xFD:
		dec, err := zstd.NewReader(f)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("open %s: %w", file, err)
		}
		return &zstdReadCloser{dec: dec, file: f}, nil
	case n >= 2 && magic[0] == 0x1f && magic[1] == 0x8b:
		dec, err := gzip.NewReader(f)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("open %s: %w", file, err)
		}
		return &gzipReadCloser{Reader: dec, file: f}, nil
	default:
		return f, nil
	}
}

type zstdReadCloser struct {
	dec  *zstd.Decoder
	file *os.File
}

func (z *zstdReadCloser) Read(p []byte) (int, error) {
	return z.dec.Read(p)
}

func (z *zstdReadCloser) Close() error {
	z.dec.Close()
	return z.file.Close()
}

type gzipReadCloser struct {
	*gzip.Reader
	file *os.File
}

func (g *gzipReadCloser) Close() error {
	err := g.Reader.Close()
	if closeErr := g.file.Close(); err == nil {
		err = closeErr
	}
	return err
}
