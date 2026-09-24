// Package hf pulls AI model artifacts from Hugging Face Hub registries.
package hf

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/modelpull"
	"github.com/eclipse-iofog/edgelet/internal/models"
)

// Adapter pulls Hugging Face Hub artifacts into {metadata.name}/content/.
type Adapter struct {
	// HTTPClient, when set, is used instead of the registry-derived client (tests).
	HTTPClient *http.Client
	// RepoClass selects /api/models/ (default) or /api/datasets/. Knowledge always uses dataset.
	RepoClass RepoClass
}

// Pull fetches Hub files, writes them under content/, and records manifest.json.
// Hugging Face pulls never write oci-store/.
func (a *Adapter) Pull(ctx context.Context, req modelpull.Request) (*modelpull.Result, error) {
	if err := a.validateRequest(req); err != nil {
		return nil, err
	}
	resolved := modelpull.ResolveHFRevision(req.Revision)

	class := a.repoClass()
	client, err := newHubClient(req.Registry, a.httpClient(), class)
	if err != nil {
		return nil, err
	}

	info, err := client.RevisionInfo(ctx, req.Repo, resolved.Revision)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(info.SHA) == "" {
		return nil, fmt.Errorf("hub returned no commit for %s at revision %s", req.Repo, resolved.Revision)
	}

	repoFiles := make([]string, 0, len(info.Files))
	sizeByPath := make(map[string]int64, len(info.Files))
	for _, f := range info.Files {
		name := normalizeRepoPath(f.Path)
		if name == "" {
			continue
		}
		repoFiles = append(repoFiles, name)
		sizeByPath[name] = f.Size
	}

	selected, err := resolvePullFiles(ctx, client, req.Repo, resolved.Revision, repoFiles, req.Files, class)
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		if class == RepoClassDataset {
			return nil, fmt.Errorf("no files in dataset %s at revision %s", req.Repo, resolved.Revision)
		}
		return nil, fmt.Errorf("no model files selected from %s at revision %s; set spec.files to the paths to pull", req.Repo, resolved.Revision)
	}

	var estimated int64
	for _, filePath := range selected {
		if sizeByPath[filePath] > 0 {
			estimated += sizeByPath[filePath]
		}
	}
	if req.Disk != nil {
		if err := req.Disk.Ensure(estimated); err != nil {
			return nil, err
		}
	}
	req.ReportProgress(0, estimated)

	contentDir := modelpull.ContentDir(req.ModelsRoot, req.Name)
	tmpContent := modelpull.ContentStagingDir(req.ModelsRoot, req.Name)
	if err := os.MkdirAll(tmpContent, 0o755); err != nil { // #nosec G301 -- staging content dir must be traversable
		return nil, fmt.Errorf("create content directory: %w", err)
	}

	var totalBytes int64
	var downloaded int64
	relPaths := make([]string, 0, len(selected))
	for _, filePath := range selected {
		dest, joinErr := safeJoin(tmpContent, filePath)
		if joinErr != nil {
			return nil, joinErr
		}
		err := client.Download(ctx, req.Repo, resolved.Revision, filePath, dest, sizeByPath[filePath], func(n int64) {
			downloaded += n
			req.ReportProgress(downloaded, estimated)
		})
		if err != nil {
			return nil, err
		}
		fi, statErr := os.Stat(dest)
		if statErr != nil {
			_ = os.RemoveAll(tmpContent)
			return nil, fmt.Errorf("stat downloaded file %q: %w", filePath, statErr)
		}
		totalBytes += fi.Size()
		relPaths = append(relPaths, filepath.ToSlash(filepath.Join(modelpull.ContentDirName, filePath)))
	}

	if err := replaceDir(contentDir, tmpContent); err != nil {
		_ = os.RemoveAll(tmpContent)
		return nil, err
	}

	formatHint := inferPulledFormat(req.FormatHint, selected)
	if class == RepoClassDataset {
		formatHint = inferKnowledgeFormat(req.FormatHint)
	}
	manifestPath := modelpull.ManifestPath(req.ModelsRoot, req.Name)
	onDisk := modelpull.OnDiskManifest{
		MetadataName:      req.Name,
		RegistryID:        req.Registry.ID,
		RegistryType:      models.RegistryTypeHF,
		Repo:              req.Repo,
		RequestedRevision: resolved.Requested,
		ResolvedRevision:  info.SHA,
		Format:            formatHint,
		Files:             selected,
		ContentPaths:      relPaths,
		TotalBytes:        totalBytes,
		RevisionFloating:  resolved.Floating,
		RevisionKind:      resolved.Kind,
	}
	if err := modelpull.WriteOnDiskManifest(manifestPath, onDisk); err != nil {
		return nil, err
	}

	return &modelpull.Result{
		ResolvedRevision: info.SHA,
		RevisionFloating: resolved.Floating,
		RevisionKind:     resolved.Kind,
		Format:           formatHint,
		ContentPaths:     relPaths,
		TotalBytes:       totalBytes,
		ManifestPath:     manifestPath,
		ContentPath:      contentDir,
	}, nil
}

func resolvePullFiles(ctx context.Context, client *hubClient, repo, revision string, repoFiles, requested []string, class RepoClass) ([]string, error) {
	if !hasRequestedFiles(requested) {
		if class == RepoClassDataset {
			return uniqueSorted(repoFiles), nil
		}
		if err := GuardMultiGGUF(repoFiles); err != nil {
			return nil, err
		}
		return expandSnapshot(ctx, client, repo, revision, repoFiles)
	}
	return ExpandFiles(requested, repoFiles)
}

func inferKnowledgeFormat(hint string) string {
	hint = strings.ToLower(strings.TrimSpace(hint))
	if hint != "" {
		return hint
	}
	return models.KnowledgeFormatUnknown
}

func expandSnapshot(ctx context.Context, client *hubClient, repo, revision string, repoFiles []string) ([]string, error) {
	selected := snapshotSidecars(repoFiles)
	var extras []string
	for _, name := range selected {
		if !strings.HasSuffix(path.Base(name), ".index.json") {
			continue
		}
		raw, err := client.FetchBytes(ctx, repo, revision, name)
		if err != nil {
			return nil, err
		}
		shards, err := shardsFromIndex(name, raw, repoFiles)
		if err != nil {
			return nil, err
		}
		extras = append(extras, shards...)
	}
	return uniqueSorted(append(selected, extras...)), nil
}

func inferPulledFormat(hint string, files []string) string {
	hint = strings.ToLower(strings.TrimSpace(hint))
	if hint != "" {
		return hint
	}
	var hasGGUF, hasST, hasONNX, hasPT bool
	for _, name := range files {
		ext := strings.ToLower(path.Ext(name))
		switch ext {
		case ".gguf":
			hasGGUF = true
		case ".safetensors":
			hasST = true
		case ".onnx":
			hasONNX = true
		case ".bin":
			base := strings.ToLower(path.Base(name))
			if strings.Contains(base, "pytorch") || strings.HasPrefix(base, "model") {
				hasPT = true
			}
		}
	}
	switch {
	case hasGGUF:
		return models.ModelFormatGGUF
	case hasST:
		return models.ModelFormatSafetensors
	case hasONNX:
		return models.ModelFormatONNX
	case hasPT:
		return models.ModelFormatPyTorch
	default:
		return models.ModelFormatUnknown
	}
}

func (a *Adapter) httpClient() *http.Client {
	if a == nil {
		return nil
	}
	return a.HTTPClient
}

func (a *Adapter) repoClass() RepoClass {
	if a == nil {
		return RepoClassModel
	}
	return NormalizeRepoClass(a.RepoClass)
}

func (a *Adapter) validateRequest(req modelpull.Request) error {
	noun := "model"
	if a.repoClass() == RepoClassDataset {
		noun = "knowledge"
	}
	if strings.TrimSpace(req.Name) == "" {
		return fmt.Errorf("%s name is required", noun)
	}
	if strings.TrimSpace(req.Repo) == "" {
		return fmt.Errorf("%s repo is required", noun)
	}
	if strings.TrimSpace(req.ModelsRoot) == "" {
		if noun == "knowledge" {
			return errors.New("knowledge root is required")
		}
		return errors.New("models root is required")
	}
	if req.Registry == nil {
		return errors.New("registry is required")
	}
	req.Registry.NormalizeDefaults()
	if req.Registry.NormalizedType() != models.RegistryTypeHF {
		return fmt.Errorf("registry type %q cannot pull Hugging Face artifacts", req.Registry.NormalizedType())
	}
	return nil
}
