// Package oci pulls AI model artifacts from OCI registries.
package oci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/modelpull"
	"github.com/eclipse-iofog/edgelet/internal/modelpull/format"
	"github.com/eclipse-iofog/edgelet/internal/modelpull/ocistore"
	"github.com/eclipse-iofog/edgelet/internal/models"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Adapter pulls OCI AI artifacts into the shared model store and materializes content/.
type Adapter struct {
	// HTTPClient, when set, is used instead of the registry-derived client (tests).
	HTTPClient *http.Client
	// Source, when set, skips opening a remote repository (tests).
	Source ArtifactSource
}

// Pull fetches the artifact, writes the shared store, and materializes content/.
// spec.files is ignored for OCI artifacts; the full artifact is always extracted.
func (a *Adapter) Pull(ctx context.Context, req modelpull.Request) (*modelpull.Result, error) {
	if err := validateRequest(req); err != nil {
		return nil, err
	}
	resolved := modelpull.ResolveOCIRevision(req.Repo, req.Revision)

	src, err := a.openSource(req.Registry, req.Repo)
	if err != nil {
		return nil, err
	}

	store, err := ocistore.Open(modelpull.OCIStoreDir(req.ModelsRoot))
	if err != nil {
		return nil, err
	}

	rootDesc, err := src.Resolve(ctx, resolved.Reference)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", resolved.Tag, err)
	}

	manifestDesc, manifestRaw, ociManifest, err := fetchManifest(ctx, src, rootDesc)
	if err != nil {
		return nil, err
	}

	kind := format.Detect(ociManifest)

	blobDescs := make([]ocispec.Descriptor, 0, 1+len(ociManifest.Layers))
	blobDescs = append(blobDescs, ociManifest.Config)
	blobDescs = append(blobDescs, ociManifest.Layers...)

	files := make([]string, 0, len(blobDescs))
	var totalBytes int64
	for _, desc := range blobDescs {
		if desc.Digest == "" {
			continue
		}
		files = append(files, string(desc.Digest))
		if desc.Size > 0 {
			totalBytes += desc.Size
		}
	}
	if req.Disk != nil {
		if err := req.Disk.Ensure(totalBytes); err != nil {
			return nil, err
		}
	}
	req.ReportProgress(0, totalBytes)

	var downloaded int64
	for _, desc := range blobDescs {
		if desc.Digest == "" {
			continue
		}
		already, _ := store.IncompleteSize(string(desc.Digest))
		if has, _ := store.HasBlob(string(desc.Digest)); has && desc.Size > 0 {
			already = desc.Size
		}
		downloaded += already
		req.ReportProgress(downloaded, totalBytes)
		if err := pullBlob(ctx, src, store, desc, func(n int64) {
			downloaded += n
			req.ReportProgress(downloaded, totalBytes)
		}); err != nil {
			return nil, err
		}
	}

	digest := string(manifestDesc.Digest)
	if err := store.WriteManifest(digest, manifestRaw, []string{resolved.Tag}, files); err != nil {
		return nil, err
	}
	if err := store.Track(req.Name, digest, files); err != nil {
		return nil, err
	}

	contentDir := modelpull.ContentDir(req.ModelsRoot, req.Name)
	tmpContent := contentDir + ".tmp"
	_ = os.RemoveAll(tmpContent)
	if err := os.MkdirAll(tmpContent, 0o755); err != nil { // #nosec G301 -- staging content dir must be traversable
		return nil, fmt.Errorf("create content directory: %w", err)
	}

	unpacked, err := format.Unpack(kind, ociManifest, store, tmpContent)
	if err != nil {
		_ = os.RemoveAll(tmpContent)
		return nil, err
	}
	if err := replaceDir(contentDir, tmpContent); err != nil {
		_ = os.RemoveAll(tmpContent)
		return nil, err
	}

	formatHint := strings.ToLower(strings.TrimSpace(req.FormatHint))
	if formatHint == "" && unpacked != nil {
		formatHint = unpacked.Format
	}
	if formatHint == "" {
		formatHint = models.ModelFormatUnknown
	}

	var configJSON json.RawMessage
	if ociManifest.Config.Digest != "" {
		if raw, readErr := store.ReadBlob(string(ociManifest.Config.Digest)); readErr == nil {
			configJSON = json.RawMessage(raw)
		}
	}

	relPaths := []string{}
	if unpacked != nil {
		relPaths = unpacked.RelPaths
	}
	filesOnDisk := make([]string, 0, len(relPaths))
	for _, p := range relPaths {
		filesOnDisk = append(filesOnDisk, strings.TrimPrefix(p, "content/"))
	}

	manifestPath := modelpull.ManifestPath(req.ModelsRoot, req.Name)
	onDisk := modelpull.OnDiskManifest{
		MetadataName:      req.Name,
		RegistryID:        req.Registry.ID,
		RegistryType:      models.RegistryTypeOCI,
		Repo:              req.Repo,
		RequestedRevision: resolved.Requested,
		ResolvedRevision:  resolved.ResolvedRevision,
		Digest:            digest,
		Format:            formatHint,
		Files:             filesOnDisk,
		ContentPaths:      relPaths,
		Blobs:             files,
		TotalBytes:        totalBytes,
		RevisionFloating:  resolved.Floating,
		RevisionKind:      resolved.Kind,
		OCIModelConfig:    configJSON,
	}
	if err := modelpull.WriteOnDiskManifest(manifestPath, onDisk); err != nil {
		return nil, err
	}

	return &modelpull.Result{
		Digest:           digest,
		ResolvedRevision: resolved.ResolvedRevision,
		RevisionFloating: resolved.Floating,
		RevisionKind:     resolved.Kind,
		Format:           formatHint,
		ContentPaths:     relPaths,
		TotalBytes:       totalBytes,
		ManifestPath:     manifestPath,
		ContentPath:      contentDir,
		OCIModelConfig:   configJSON,
		Blobs:            files,
	}, nil
}

func validateRequest(req modelpull.Request) error {
	if strings.TrimSpace(req.Name) == "" {
		return errors.New("model name is required")
	}
	if strings.TrimSpace(req.Repo) == "" {
		return errors.New("model repo is required")
	}
	if strings.TrimSpace(req.ModelsRoot) == "" {
		return errors.New("models root is required")
	}
	if req.Registry == nil {
		return errors.New("registry is required")
	}
	req.Registry.NormalizeDefaults()
	if req.Registry.NormalizedType() != models.RegistryTypeOCI {
		return fmt.Errorf("registry type %q cannot pull OCI artifacts", req.Registry.NormalizedType())
	}
	return nil
}

func (a *Adapter) openSource(reg *models.Registry, repo string) (ArtifactSource, error) {
	if a != nil && a.Source != nil {
		return a.Source, nil
	}
	var httpClient *http.Client
	if a != nil {
		httpClient = a.HTTPClient
	}
	remoteRepo, authClient, ep, err := newRemoteRepository(reg, repo, httpClient)
	if err != nil {
		return nil, err
	}
	return &orasSource{repo: remoteRepo, client: authClient, ep: ep}, nil
}

func fetchManifest(ctx context.Context, src ArtifactSource, desc ocispec.Descriptor) (ocispec.Descriptor, []byte, ocispec.Manifest, error) {
	raw, err := fetchJSONDescriptor(ctx, src, desc)
	if err != nil {
		return ocispec.Descriptor{}, nil, ocispec.Manifest{}, fmt.Errorf("fetch manifest: %w", err)
	}
	if isIndexMedia(desc.MediaType) {
		var idx ocispec.Index
		if err := json.Unmarshal(raw, &idx); err != nil {
			return ocispec.Descriptor{}, nil, ocispec.Manifest{}, fmt.Errorf("decode image index: %w", err)
		}
		if len(idx.Manifests) == 0 {
			return ocispec.Descriptor{}, nil, ocispec.Manifest{}, errors.New("image index has no manifests")
		}
		return fetchManifest(ctx, src, idx.Manifests[0])
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return ocispec.Descriptor{}, nil, ocispec.Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	return desc, raw, manifest, nil
}

func isIndexMedia(mediaType string) bool {
	mt := strings.ToLower(mediaType)
	return mt == ocispec.MediaTypeImageIndex || mt == "application/vnd.docker.distribution.manifest.list.v2+json"
}

func pullBlob(ctx context.Context, src ArtifactSource, store *ocistore.Store, desc ocispec.Descriptor, onWrite func(n int64)) error {
	digest := string(desc.Digest)
	if digest == "" {
		return errors.New("blob descriptor is missing a digest")
	}
	has, err := store.HasBlob(digest)
	if err != nil {
		return err
	}
	if has {
		return nil
	}

	incomplete, err := store.IncompleteSize(digest)
	if err != nil {
		return err
	}
	wrap := func(rc io.ReadCloser, already int64) io.Reader {
		if onWrite == nil {
			return rc
		}
		seen := already
		return modelpull.WatchReader(rc, already, desc.Size, func(downloaded, _ int64) {
			if downloaded > seen {
				onWrite(downloaded - seen)
				seen = downloaded
			}
		})
	}
	if incomplete > 0 {
		rc, resumed, fetchErr := src.FetchRange(ctx, desc, incomplete)
		if fetchErr != nil {
			return fmt.Errorf("resume blob %s: %w", digest, fetchErr)
		}
		defer func() {
			_ = rc.Close()
		}()
		if resumed {
			return store.AppendBlob(digest, wrap(rc, incomplete), desc.Size)
		}
		_ = store.RemoveIncomplete(digest)
		return store.WriteBlob(digest, wrap(rc, 0), desc.Size)
	}

	rc, err := src.Fetch(ctx, desc)
	if err != nil {
		return fmt.Errorf("fetch blob %s: %w", digest, err)
	}
	defer func() {
		_ = rc.Close()
	}()
	return store.WriteBlob(digest, wrap(rc, 0), desc.Size)
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
