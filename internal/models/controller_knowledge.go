package models

import (
	"encoding/json"
	"strings"
)

// ControllerKnowledge is a fleet-desired Knowledge row. Identity is uuid plus unique name.
type ControllerKnowledge struct {
	UUID             string `json:"uuid"`
	Name             string `json:"name"`
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

// NormalizeDefaults trims ControllerKnowledge identity fields.
func (k *ControllerKnowledge) NormalizeDefaults() {
	if k == nil {
		return
	}
	k.UUID = strings.TrimSpace(k.UUID)
	k.Name = strings.TrimSpace(k.Name)
	k.Repo = strings.TrimSpace(k.Repo)
	k.Revision = strings.TrimSpace(k.Revision)
	k.Format = strings.ToLower(strings.TrimSpace(k.Format))
	if strings.TrimSpace(k.FilesJSON) == "" {
		k.FilesJSON = "[]"
	}
	if strings.TrimSpace(k.State) == "" {
		k.State = KnowledgeStatePending
	}
	if k.TotalBytes < 0 {
		k.TotalBytes = 0
	}
}

// Files returns the decoded spec.files list (empty slice when unset).
func (k *ControllerKnowledge) Files() []string {
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
func (k *ControllerKnowledge) SetFiles(files []string) {
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

// ToLocalKnowledge builds a Pending local row from a controller-desired Knowledge.
func (k *ControllerKnowledge) ToLocalKnowledge() *LocalKnowledge {
	if k == nil {
		return nil
	}
	row := &LocalKnowledge{
		Name:       strings.TrimSpace(k.Name),
		Source:     KnowledgeSourceManaged,
		Repo:       strings.TrimSpace(k.Repo),
		Revision:   strings.TrimSpace(k.Revision),
		RegistryID: k.RegistryID,
		Format:     strings.ToLower(strings.TrimSpace(k.Format)),
		State:      KnowledgeStatePending,
	}
	row.SetFiles(k.Files())
	row.NormalizeDefaults()
	return row
}
