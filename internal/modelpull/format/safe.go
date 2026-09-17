package format

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// BlobOpener opens a stored blob by digest (sha256:…).
type BlobOpener interface {
	Open(digest string) (io.ReadCloser, error)
}

func safeJoin(root, rel string) (string, error) {
	raw := filepath.ToSlash(rel)
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("empty content path")
	}
	if strings.Contains(raw, "..") {
		return "", fmt.Errorf("content path %q escapes destination", rel)
	}
	cleaned := filepath.Clean("/" + raw)
	rel = strings.TrimPrefix(cleaned, "/")
	if rel == "" || rel == "." {
		return "", errors.New("empty content path")
	}
	dest := filepath.Join(root, filepath.FromSlash(rel))
	relToRoot, err := filepath.Rel(filepath.Clean(root), dest)
	if err != nil || strings.HasPrefix(relToRoot, "..") {
		return "", fmt.Errorf("content path %q escapes destination", rel)
	}
	return dest, nil
}

// maxTarEntryBytes caps a single tar member so a hostile archive cannot
// expand without bound while unpacking into content/.
const maxTarEntryBytes int64 = 512 << 30

func copyBlobToFile(opener BlobOpener, digest, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil { // #nosec G301 -- materialized content dir must be traversable
		return fmt.Errorf("create content directory: %w", err)
	}
	rc, err := opener.Open(digest)
	if err != nil {
		return fmt.Errorf("open blob %s: %w", digest, err)
	}
	defer func() {
		_ = rc.Close()
	}()
	f, err := os.Create(dest) // #nosec G304 -- dest is validated by safeJoin under the model content root
	if err != nil {
		return fmt.Errorf("create content file %q: %w", dest, err)
	}
	if _, err := io.Copy(f, rc); err != nil {
		_ = f.Close()
		return fmt.Errorf("write content file %q: %w", dest, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close content file %q: %w", dest, err)
	}
	return nil
}

func extractTar(r io.Reader, destDir string, gzipped bool) ([]string, error) {
	src := r
	if gzipped {
		gr, err := gzip.NewReader(r)
		if err != nil {
			return nil, fmt.Errorf("gzip reader: %w", err)
		}
		defer func() {
			_ = gr.Close()
		}()
		src = gr
	}
	tr := tar.NewReader(src)
	var paths []string
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar: %w", err)
		}
		name := strings.TrimPrefix(filepath.ToSlash(hdr.Name), "./")
		if name == "" || name == "." {
			continue
		}
		dest, err := safeJoin(destDir, name)
		if err != nil {
			return nil, err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, 0o755); err != nil { // #nosec G301 -- unpacked model dirs must be traversable
				return nil, fmt.Errorf("create tar directory: %w", err)
			}
		case tar.TypeReg:
			if hdr.Size < 0 || hdr.Size > maxTarEntryBytes {
				return nil, fmt.Errorf("tar entry %q has invalid size %d", name, hdr.Size)
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil { // #nosec G301 -- unpacked model dirs must be traversable
				return nil, fmt.Errorf("create tar parent: %w", err)
			}
			f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, hdr.FileInfo().Mode().Perm()) // #nosec G304 -- dest is validated by safeJoin
			if err != nil {
				return nil, fmt.Errorf("create tar file %q: %w", name, err)
			}
			n, err := io.CopyN(f, tr, hdr.Size)
			if err != nil && !errors.Is(err, io.EOF) {
				_ = f.Close()
				return nil, fmt.Errorf("write tar file %q: %w", name, err)
			}
			if n != hdr.Size {
				_ = f.Close()
				return nil, fmt.Errorf("tar entry %q: wrote %d bytes, expected %d", name, n, hdr.Size)
			}
			if err := f.Close(); err != nil {
				return nil, fmt.Errorf("close tar file %q: %w", name, err)
			}
			paths = append(paths, name)
		default:
			// skip links and specials
		}
	}
	return paths, nil
}

func isTarMedia(mediaType string) bool {
	mt := strings.ToLower(mediaType)
	return strings.Contains(mt, "tar")
}

func isGzipTarMedia(mediaType string) bool {
	mt := strings.ToLower(mediaType)
	return strings.Contains(mt, "tar") && (strings.Contains(mt, "gzip") || strings.Contains(mt, "+gzip") || strings.HasSuffix(mt, ".gz"))
}

func contentRel(path string) string {
	return filepath.ToSlash(filepath.Join("content", path))
}
