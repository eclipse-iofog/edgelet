package untar

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// ExtractRegularFile writes one regular file from a zstd-compressed tar to dest.
// Other archive members are skipped. dest's parent directories are created.
func ExtractRegularFile(r io.Reader, member, dest string) error {
	member = cleanTarMember(member)
	if member == "" || member == "." {
		return fmt.Errorf("invalid archive member %q", member)
	}

	zr, err := zstd.NewReader(r, zstd.WithDecoderMaxMemory(maxDecoderMemory))
	if err != nil {
		return fmt.Errorf("error extracting zstd-compressed body: %w", err)
	}
	defer func() { zr.Close() }()

	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("archive member %s not found", member)
		}
		if err != nil {
			return fmt.Errorf("tar error: %w", err)
		}
		if !validRelPath(hdr.Name) {
			return fmt.Errorf("tar contained invalid name %q", hdr.Name)
		}
		if cleanTarMember(hdr.Name) != member {
			if _, err := io.Copy(io.Discard, tr); err != nil { // #nosec G110 -- trusted embed bundle; body must be consumed
				return fmt.Errorf("skip %s: %w", hdr.Name, err)
			}
			continue
		}
		if !hdr.FileInfo().Mode().IsRegular() {
			return fmt.Errorf("archive member %s is not a regular file", member)
		}
		return writeArchiveFile(dest, tr, hdr.Size)
	}
}

func cleanTarMember(name string) string {
	name = strings.TrimPrefix(name, "./")
	name = path.Clean(name)
	if name == "." || strings.HasPrefix(name, "../") || path.IsAbs(name) {
		return ""
	}
	return name
}

func writeArchiveFile(dest string, r io.Reader, size int64) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil { // #nosec G301 -- staged runtime directory must be traversable
		return err
	}
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		return err
	}
	wf, err := os.OpenFile(dest, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o755) // #nosec G302 G304 -- staged fat runtime must be executable; dest is chosen by the caller
	if err != nil {
		return err
	}
	n, err := io.Copy(wf, r) // #nosec G110 -- trusted embed bundle; zstd decoder memory capped
	if closeErr := wf.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(dest)
		return fmt.Errorf("error writing to %s: %w", dest, err)
	}
	if n != size {
		_ = os.Remove(dest)
		return fmt.Errorf("only wrote %d bytes to %s; expected %d", n, dest, size)
	}
	if err := os.Chmod(dest, 0o755); err != nil { // #nosec G302 -- staged fat runtime must be executable
		_ = os.Remove(dest)
		return err
	}
	return nil
}
