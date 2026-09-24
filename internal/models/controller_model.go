package models

import (
	"encoding/json"
	"strings"
)

// ControllerModel is a fleet-desired Model row. Identity is uuid plus unique name.
type ControllerModel struct {
	UUID       string `json:"uuid"`
	Name       string `json:"name"`
	Repo       string `json:"repo"`
	Revision   string `json:"revision,omitempty"`
	RegistryID int    `json:"registryId"`
	FilesJSON  string `json:"filesJson,omitempty"`
	Format     string `json:"format,omitempty"`
}

// NormalizeDefaults trims ControllerModel identity fields.
func (m *ControllerModel) NormalizeDefaults() {
	if m == nil {
		return
	}
	m.UUID = strings.TrimSpace(m.UUID)
	m.Name = strings.TrimSpace(m.Name)
	m.Repo = strings.TrimSpace(m.Repo)
	m.Revision = strings.TrimSpace(m.Revision)
	m.Format = strings.ToLower(strings.TrimSpace(m.Format))
	if strings.TrimSpace(m.FilesJSON) == "" {
		m.FilesJSON = "[]"
	}
}

// Files returns the decoded spec.files list (empty slice when unset).
func (m *ControllerModel) Files() []string {
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
func (m *ControllerModel) SetFiles(files []string) {
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

// ToLocalModel builds a Pending local row from a controller-desired Model.
func (m *ControllerModel) ToLocalModel() *LocalModel {
	if m == nil {
		return nil
	}
	row := &LocalModel{
		Name:       strings.TrimSpace(m.Name),
		Source:     ModelSourceManaged,
		Repo:       strings.TrimSpace(m.Repo),
		Revision:   strings.TrimSpace(m.Revision),
		RegistryID: m.RegistryID,
		Format:     strings.ToLower(strings.TrimSpace(m.Format)),
		State:      ModelStatePending,
	}
	row.SetFiles(m.Files())
	row.NormalizeDefaults()
	return row
}
