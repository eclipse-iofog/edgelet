package modelpull

import (
	"encoding/json"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

// Request is the input for an artifact pull.
type Request struct {
	// Name is metadata.name and the on-disk directory under the models root.
	Name string
	// Repo is spec.repo (path only, no host).
	Repo string
	// Revision is spec.revision (tag, digest, or empty).
	Revision string
	// Registry is the source registry row. Type must match the pull adapter.
	Registry *models.Registry
	// Files is spec.files (Hub pulls only; ignored for OCI artifacts).
	Files []string
	// ModelsRoot is {diskDirectory}/models.
	ModelsRoot string
	// FormatHint is optional spec.format.
	FormatHint string
	// Disk, when set, is consulted after size is known and before payload download.
	Disk DiskGuard
	// OnProgress reports bytes copied so far (and total when known).
	OnProgress func(downloaded, total int64)
}

// Result is the outcome of a successful pull and materialize.
type Result struct {
	Digest           string
	ResolvedRevision string
	RevisionFloating bool
	RevisionKind     string
	Format           string
	ContentPaths     []string
	TotalBytes       int64
	ManifestPath     string
	ContentPath      string
	OCIModelConfig   json.RawMessage
	Blobs            []string
}
