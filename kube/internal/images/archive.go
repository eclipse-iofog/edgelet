package images

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
)

func isArchive(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	switch {
	case strings.HasSuffix(name, ".tar.gz"), strings.HasSuffix(name, ".tgz"):
		return true
	case strings.HasSuffix(name, ".tar.zst"):
		return true
	case strings.HasSuffix(name, ".tar"):
		return true
	default:
		return false
	}
}

func openArchive(path string) (io.ReadCloser, error) {
	file, err := os.Open(path) // #nosec G304 -- path is an image archive chosen for import
	if err != nil {
		return nil, err
	}
	name := strings.ToLower(filepath.Base(path))
	switch {
	case strings.HasSuffix(name, ".tar.gz"), strings.HasSuffix(name, ".tgz"):
		reader, err := gzip.NewReader(file)
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("open %s: %w", path, err)
		}
		return &multiCloser{Reader: reader, closers: []io.Closer{reader, file}}, nil
	case strings.HasSuffix(name, ".tar.zst"):
		reader, err := zstd.NewReader(file)
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("open %s: %w", path, err)
		}
		return &multiCloser{Reader: reader, closers: []io.Closer{zstdCloser{reader}, file}}, nil
	case strings.HasSuffix(name, ".tar"):
		return file, nil
	default:
		_ = file.Close()
		return nil, fmt.Errorf("open %s: not an image archive", path)
	}
}

type zstdCloser struct {
	*zstd.Decoder
}

func (z zstdCloser) Close() error {
	z.Decoder.Close()
	return nil
}

type multiCloser struct {
	io.Reader
	closers []io.Closer
}

func (m *multiCloser) Close() error {
	var err error
	for _, closer := range m.closers {
		err = errors.Join(err, closer.Close())
	}
	return err
}
