//revive:disable:nested-structs
package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// LocalKnowledgeSpec is the spec block of a local Knowledge deploy document.
type LocalKnowledgeSpec struct {
	Repo     string   `yaml:"repo" json:"repo"`
	Revision string   `yaml:"revision,omitempty" json:"revision,omitempty"`
	Registry int      `yaml:"registry" json:"registry"`
	Files    []string `yaml:"files,omitempty" json:"files,omitempty"`
	Format   string   `yaml:"format,omitempty" json:"format,omitempty"`
}

// LocalKnowledgeManifest represents local Knowledge deployment YAML.
type LocalKnowledgeManifest struct {
	APIVersion string `yaml:"apiVersion" json:"apiVersion"`
	Kind       string `yaml:"kind" json:"kind"`
	Metadata   struct {
		Name   string            `yaml:"name" json:"name"`
		Labels map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
	} `yaml:"metadata" json:"metadata"`
	Spec LocalKnowledgeSpec `yaml:"spec" json:"spec"`
}

func (m *LocalKnowledgeManifest) Validate() error {
	return m.ValidateWithRegistry(nil)
}

// ValidateWithRegistry validates the Knowledge document. When reg is non-nil (row exists),
// it checks id match and registry type compatibility.
func (m *LocalKnowledgeManifest) ValidateWithRegistry(reg *Registry) error {
	if m == nil {
		return errors.New("manifest is nil")
	}
	if strings.TrimSpace(m.APIVersion) == "" {
		return errors.New("apiVersion is required")
	}
	if strings.TrimSpace(m.Kind) == "" {
		return errors.New("kind is required")
	}
	if strings.TrimSpace(m.Kind) != "Knowledge" {
		return errors.New("kind must be Knowledge")
	}
	switch strings.TrimSpace(m.APIVersion) {
	case "edgelet.iofog.org/v1":
	default:
		return errors.New("apiVersion must be edgelet.iofog.org/v1")
	}

	name := strings.TrimSpace(m.Metadata.Name)
	if name == "" {
		return errors.New("metadata.name is required")
	}
	if strings.Contains(name, "/") {
		return errors.New("metadata.name must be a DNS-1123 label and must not contain '/'")
	}
	if len(name) > 63 {
		return errors.New("metadata.name must be <= 63 characters and follow DNS-1123 label format")
	}
	if !localModelNamePattern.MatchString(name) {
		return errors.New("metadata.name must match DNS-1123 label format: lowercase alphanumeric or '-', start/end alphanumeric")
	}
	m.Metadata.Name = name

	if err := validateModelRepo(m.Spec.Repo); err != nil {
		return err
	}
	m.Spec.Repo = strings.TrimSpace(m.Spec.Repo)
	m.Spec.Revision = strings.TrimSpace(m.Spec.Revision)
	m.Spec.Format = strings.ToLower(strings.TrimSpace(m.Spec.Format))

	if m.Spec.Registry <= 0 {
		return errors.New("spec.registry is required")
	}
	if !ValidKnowledgeFormat(m.Spec.Format) {
		return errors.New("spec.format must be one of markdown, pdf, jsonl, parquet, arrow, sqlite, faiss, chroma, lance, unknown")
	}

	if reg == nil {
		return nil
	}
	reg.NormalizeDefaults()
	if reg.ID != m.Spec.Registry {
		return fmt.Errorf("spec.registry %d does not match registry row id %d", m.Spec.Registry, reg.ID)
	}
	if !ValidRegistryType(reg.Type) {
		return fmt.Errorf("registry %d has unsupported type %q", reg.ID, reg.Type)
	}
	// spec.files is HF-only; OCI ignores the list — not an error.
	return nil
}

// ToLocalKnowledge builds a store row from a validated manifest.
func (m *LocalKnowledgeManifest) ToLocalKnowledge() *LocalKnowledge {
	if m == nil {
		return nil
	}
	row := &LocalKnowledge{
		Name:       strings.TrimSpace(m.Metadata.Name),
		Source:     KnowledgeSourceLocal,
		Repo:       strings.TrimSpace(m.Spec.Repo),
		Revision:   strings.TrimSpace(m.Spec.Revision),
		RegistryID: m.Spec.Registry,
		Format:     strings.ToLower(strings.TrimSpace(m.Spec.Format)),
		State:      KnowledgeStatePending,
	}
	row.SetFiles(m.Spec.Files)
	row.NormalizeDefaults()
	return row
}

// FilesJSON returns the spec.files list encoded for SQLite.
func (m *LocalKnowledgeManifest) FilesJSON() string {
	if m == nil || m.Spec.Files == nil {
		return "[]"
	}
	raw, err := json.Marshal(m.Spec.Files)
	if err != nil {
		return "[]"
	}
	return string(raw)
}
