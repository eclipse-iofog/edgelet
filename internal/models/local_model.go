package models

import (
	"encoding/json"
	"strings"
	"time"
)

// Model lifecycle states.
const (
	ModelStatePending = "Pending"
	ModelStatePulling = "Pulling"
	ModelStateReady   = "Ready"
	ModelStateFailed  = "Failed"
)

// Optional spec.format hints.
const (
	ModelFormatGGUF        = "gguf"
	ModelFormatSafetensors = "safetensors"
	ModelFormatONNX        = "onnx"
	ModelFormatPyTorch     = "pytorch"
	ModelFormatTensorRT    = "tensorrt"
	ModelFormatUnknown     = "unknown"
)

// LocalModel is the persistent local Model row stored in SQLite.
type LocalModel struct {
	Name               string `json:"name"`
	Source             string `json:"source,omitempty"`
	Repo               string `json:"repo"`
	Revision           string `json:"revision,omitempty"`
	RegistryID         int    `json:"registryId"`
	FilesJSON          string `json:"filesJson,omitempty"`
	Format             string `json:"format,omitempty"`
	State              string `json:"state"`
	LastError          string `json:"lastError,omitempty"`
	Generation         int64  `json:"generation"`
	ObservedGeneration int64  `json:"observedGeneration"`
	ManifestYAML       string `json:"manifestYaml,omitempty"`
	ManifestPath       string `json:"manifestPath,omitempty"`
	ContentPath        string `json:"contentPath,omitempty"`
	ResolvedRevision   string `json:"resolvedRevision,omitempty"`
	Digest             string `json:"digest,omitempty"`
	RevisionFloating   bool   `json:"revisionFloating,omitempty"`
	TotalBytes         int64  `json:"totalBytes,omitempty"`
	LastTransitionAt   int64  `json:"lastTransitionAt,omitempty"`
	LastReconcileAt    int64  `json:"lastReconcileAt,omitempty"`
	PulledAt           int64  `json:"pulledAt,omitempty"`
}

// NormalizeDefaults applies Model row defaults used by store accessors.
func (m *LocalModel) NormalizeDefaults() {
	if m == nil {
		return
	}
	m.Name = strings.TrimSpace(m.Name)
	m.Source = strings.ToLower(strings.TrimSpace(m.Source))
	if m.Source == "" {
		m.Source = ModelSourceLocal
	}
	m.Repo = strings.TrimSpace(m.Repo)
	m.Revision = strings.TrimSpace(m.Revision)
	m.Format = strings.ToLower(strings.TrimSpace(m.Format))
	if strings.TrimSpace(m.State) == "" {
		m.State = ModelStatePending
	}
	if strings.TrimSpace(m.FilesJSON) == "" {
		m.FilesJSON = "[]"
	}
	if m.Generation <= 0 {
		m.Generation = 1
	}
	if m.ObservedGeneration < 0 {
		m.ObservedGeneration = 0
	}
	if m.LastTransitionAt <= 0 {
		m.LastTransitionAt = time.Now().Unix()
	}
	if m.LastReconcileAt < 0 {
		m.LastReconcileAt = 0
	}
	if m.PulledAt < 0 {
		m.PulledAt = 0
	}
	if m.TotalBytes < 0 {
		m.TotalBytes = 0
	}
}

// Files returns the decoded spec.files list (empty slice when unset).
func (m *LocalModel) Files() []string {
	if m == nil || strings.TrimSpace(m.FilesJSON) == "" || m.FilesJSON == "[]" {
		return []string{}
	}
	var files []string
	if err := json.Unmarshal([]byte(m.FilesJSON), &files); err != nil || files == nil {
		return []string{}
	}
	return files
}

// SetFiles encodes spec.files as JSON for persistence.
func (m *LocalModel) SetFiles(files []string) {
	if m == nil {
		return
	}
	if files == nil {
		m.FilesJSON = "[]"
		return
	}
	raw, err := json.Marshal(files)
	if err != nil {
		m.FilesJSON = "[]"
		return
	}
	m.FilesJSON = string(raw)
}

// ValidModelFormat reports whether format is empty or a known hint.
func ValidModelFormat(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", ModelFormatGGUF, ModelFormatSafetensors, ModelFormatONNX, ModelFormatPyTorch, ModelFormatTensorRT, ModelFormatUnknown:
		return true
	default:
		return false
	}
}

// ValidModelState reports whether state is a known Model lifecycle value.
func ValidModelState(state string) bool {
	switch strings.TrimSpace(state) {
	case ModelStatePending, ModelStatePulling, ModelStateReady, ModelStateFailed:
		return true
	default:
		return false
	}
}
