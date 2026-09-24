package oci

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
)

// ArtifactSource resolves and fetches OCI descriptors from a registry.
type ArtifactSource interface {
	Resolve(ctx context.Context, reference string) (ocispec.Descriptor, error)
	Fetch(ctx context.Context, desc ocispec.Descriptor) (io.ReadCloser, error)
	FetchRange(ctx context.Context, desc ocispec.Descriptor, offset int64) (io.ReadCloser, bool, error)
}

type orasSource struct {
	repo   *remote.Repository
	client *auth.Client
	ep     registryEndpoint
}

func (s *orasSource) Resolve(ctx context.Context, reference string) (ocispec.Descriptor, error) {
	return s.repo.Resolve(ctx, reference)
}

func (s *orasSource) Fetch(ctx context.Context, desc ocispec.Descriptor) (io.ReadCloser, error) {
	return s.repo.Fetch(ctx, desc)
}

func (s *orasSource) FetchRange(ctx context.Context, desc ocispec.Descriptor, offset int64) (io.ReadCloser, bool, error) {
	if offset <= 0 {
		rc, err := s.Fetch(ctx, desc)
		return rc, false, err
	}
	blobURL := fmt.Sprintf("%s://%s/v2/%s/blobs/%s", s.ep.Scheme, s.ep.Host, repositoryPath(s.ep.RepoRef, s.ep.Host), desc.Digest)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, blobURL, nil)
	if err != nil {
		return nil, false, fmt.Errorf("create range request: %w", err)
	}
	req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("range fetch: %w", err)
	}
	if resp.StatusCode == http.StatusPartialContent {
		return resp.Body, true, nil
	}
	// Registry ignored Range; caller should restart the blob from offset 0.
	if resp.StatusCode == http.StatusOK {
		return resp.Body, false, nil
	}
	_ = resp.Body.Close()
	return nil, false, fmt.Errorf("range fetch: unexpected status %s", resp.Status)
}

func repositoryPath(repoRef, host string) string {
	trimmed := strings.TrimPrefix(repoRef, host+"/")
	return strings.TrimPrefix(trimmed, "/")
}

func fetchJSONDescriptor(ctx context.Context, src ArtifactSource, desc ocispec.Descriptor) ([]byte, error) {
	rc, err := src.Fetch(ctx, desc)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rc.Close()
	}()
	return content.ReadAll(rc, desc)
}
