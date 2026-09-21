package models

import (
	"encoding/json"
	"strings"
)

// Knowledge lifecycle states.
const (
	KnowledgeStatePending = "Pending"
	KnowledgeStatePulling = "Pulling"
	KnowledgeStateReady   = "Ready"
	KnowledgeStateFailed  = "Failed"
)

// Optional spec.format hints for Knowledge artifacts.
const (
	KnowledgeFormatMarkdown = "markdown"
	KnowledgeFormatPDF      = "pdf"
	KnowledgeFormatJSONL    = "jsonl"
	KnowledgeFormatParquet  = "parquet"
	KnowledgeFormatArrow    = "arrow"
	KnowledgeFormatSQLite   = "sqlite"
	KnowledgeFormatFaiss    = "faiss"
	KnowledgeFormatChroma   = "chroma"
	KnowledgeFormatLance    = "lance"
	KnowledgeFormatUnknown  = "unknown"
)

// LocalKnowledge is the persistent local Knowledge row stored in SQLite.
type LocalKnowledge struct {
	Name             string `json:"name"`
	Generation       int64  `json:"generation"`
	Source           string `json:"source,omitempty"`
	RegistryID       int    `json:"registryId"`
	Repo             string `json:"repo"`
	Revision         string `json:"revision,omitempty"`
	FilesJSON        string `json:"filesJson,omitempty"`
	Format           string `json:"format,omitempty"`
	State            string `json:"state"`
	ResolvedRevision string `json:"resolvedRevision,omitempty"`
	Digest           string `json:"digest,omitempty"`
	RevisionFloating bool   `json:"revisionFloating,omitempty"`
	TotalBytes       int64  `json:"totalBytes,omitempty"`
	LastError        string `json:"lastError,omitempty"`
}

// NormalizeDefaults applies Knowledge row defaults used by store accessors.
func (k *LocalKnowledge) NormalizeDefaults() {
	if k == nil {
		return
	}
	k.Name = strings.TrimSpace(k.Name)
	k.Source = strings.ToLower(strings.TrimSpace(k.Source))
	if k.Source == "" {
		k.Source = KnowledgeSourceLocal
	}
	k.Repo = strings.TrimSpace(k.Repo)
	k.Revision = strings.TrimSpace(k.Revision)
	k.Format = strings.ToLower(strings.TrimSpace(k.Format))
	if strings.TrimSpace(k.State) == "" {
		k.State = KnowledgeStatePending
	}
	if strings.TrimSpace(k.FilesJSON) == "" {
		k.FilesJSON = "[]"
	}
	if k.Generation <= 0 {
		k.Generation = 1
	}
	if k.TotalBytes < 0 {
		k.TotalBytes = 0
	}
}

// Files returns the decoded spec.files list (empty slice when unset).
func (k *LocalKnowledge) Files() []string {
	if k == nil || strings.TrimSpace(k.FilesJSON) == "" || k.FilesJSON == "[]" {
		return []string{}
	}
	var files []string
	if err := json.Unmarshal([]byte(k.FilesJSON), &files); err != nil || files == nil {
		return []string{}
	}
	return files
}

// SetFiles encodes spec.files as JSON for persistence.
func (k *LocalKnowledge) SetFiles(files []string) {
	if k == nil {
		return
	}
	if files == nil {
		k.FilesJSON = "[]"
		return
	}
	raw, err := json.Marshal(files)
	if err != nil {
		k.FilesJSON = "[]"
		return
	}
	k.FilesJSON = string(raw)
}

// ValidKnowledgeFormat reports whether format is empty or a known Knowledge hint.
func ValidKnowledgeFormat(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "",
		KnowledgeFormatMarkdown,
		KnowledgeFormatPDF,
		KnowledgeFormatJSONL,
		KnowledgeFormatParquet,
		KnowledgeFormatArrow,
		KnowledgeFormatSQLite,
		KnowledgeFormatFaiss,
		KnowledgeFormatChroma,
		KnowledgeFormatLance,
		KnowledgeFormatUnknown:
		return true
	default:
		return false
	}
}

// ValidKnowledgeState reports whether state is a known Knowledge lifecycle value.
func ValidKnowledgeState(state string) bool {
	switch strings.TrimSpace(state) {
	case KnowledgeStatePending, KnowledgeStatePulling, KnowledgeStateReady, KnowledgeStateFailed:
		return true
	default:
		return false
	}
}
