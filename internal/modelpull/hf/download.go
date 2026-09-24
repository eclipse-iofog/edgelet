package hf

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/modelpull"
)

func (c *hubClient) Download(ctx context.Context, repo, revision, path, dest string, expectedSize int64, onWrite func(n int64)) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil { // #nosec G301 -- Hub download dest is under the model content root
		return fmt.Errorf("create download directory: %w", err)
	}

	if complete, err := existingSize(dest); err != nil {
		return err
	} else if expectedSize > 0 && complete == expectedSize {
		if onWrite != nil {
			onWrite(complete)
		}
		return nil
	} else if complete > 0 {
		if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("reset complete download %q: %w", dest, err)
		}
	}

	partial := dest + modelpull.IncompleteExt
	existing, err := existingSize(partial)
	if err != nil {
		return err
	}
	if expectedSize > 0 && existing > expectedSize {
		if err := os.Remove(partial); err != nil {
			return fmt.Errorf("reset oversized download %q: %w", partial, err)
		}
		existing = 0
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.resolveURL(repo, revision, path), nil)
	if err != nil {
		return fmt.Errorf("create hub download request: %w", err)
	}
	c.applyHeaders(req)
	if existing > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", existing))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("hub download %s: %w", path, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body := io.Reader(resp.Body)
	if onWrite != nil {
		already := existing
		body = modelpull.WatchReader(body, existing, expectedSize, func(downloaded, _ int64) {
			if downloaded > already {
				onWrite(downloaded - already)
				already = downloaded
			}
		})
	}

	var written int64
	switch resp.StatusCode {
	case http.StatusOK:
		if existing > 0 {
			if err := os.Remove(partial); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("reset download %q: %w", partial, err)
			}
			existing = 0
		}
		written, err = writeDownload(partial, body, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
	case http.StatusPartialContent:
		if existing <= 0 {
			written, err = writeDownload(partial, body, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
		} else {
			written, err = writeDownload(partial, body, os.O_CREATE|os.O_WRONLY|os.O_APPEND)
		}
	default:
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return hubStatusError("download "+path, resp.StatusCode, raw)
	}
	if err != nil {
		return err
	}
	final := existing + written
	if expectedSize > 0 && final != expectedSize {
		return fmt.Errorf("incomplete download of %s: got %d bytes, want %d", path, final, expectedSize)
	}
	if err := os.Rename(partial, dest); err != nil {
		return fmt.Errorf("commit download %q: %w", dest, err)
	}
	return nil
}

func existingSize(path string) (int64, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("stat download %q: %w", path, err)
	}
	if fi.IsDir() {
		return 0, fmt.Errorf("download path %q is a directory", path)
	}
	return fi.Size(), nil
}

func writeDownload(dest string, r io.Reader, flags int) (int64, error) {
	f, err := os.OpenFile(dest, flags, 0o644) // #nosec G304,G302 -- dest is validated by safeJoin; weights are world-readable on the node
	if err != nil {
		return 0, fmt.Errorf("open download %q: %w", dest, err)
	}
	n, err := io.Copy(f, r)
	if err != nil {
		_ = f.Close()
		return n, fmt.Errorf("write download %q: %w", dest, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return n, fmt.Errorf("sync download %q: %w", dest, err)
	}
	if err := f.Close(); err != nil {
		return n, fmt.Errorf("close download %q: %w", dest, err)
	}
	return n, nil
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

func replaceDir(final, tmp string) error {
	if err := os.RemoveAll(final); err != nil {
		return fmt.Errorf("replace content directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil { // #nosec G301 -- per-model dir must be traversable for inspect and bind mounts
		return fmt.Errorf("create model directory: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("commit content directory: %w", err)
	}
	return nil
}
