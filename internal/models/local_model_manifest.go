//revive:disable:nested-structs
package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// LocalModelSpec is the spec block of a local Model deploy document.
type LocalModelSpec struct {
	Repo     string   `yaml:"repo" json:"repo"`
	Revision string   `yaml:"revision,omitempty" json:"revision,omitempty"`
	Registry int      `yaml:"registry" json:"registry"`
	Files    []string `yaml:"files,omitempty" json:"files,omitempty"`
	Format   string   `yaml:"format,omitempty" json:"format,omitempty"`
}

// LocalModelManifest represents local Model deployment YAML.
type LocalModelManifest struct {
	APIVersion string `yaml:"apiVersion" json:"apiVersion"`
	Kind       string `yaml:"kind" json:"kind"`
	Metadata   struct {
		Name   string            `yaml:"name" json:"name"`
		Labels map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
	} `yaml:"metadata" json:"metadata"`
	Spec LocalModelSpec `yaml:"spec" json:"spec"`
}

var localModelNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func (m *LocalModelManifest) Validate() error {
	return m.ValidateWithRegistry(nil)
}

// ValidateWithRegistry validates the Model document. When reg is non-nil (row exists),
// it checks id match and registry type compatibility.
func (m *LocalModelManifest) ValidateWithRegistry(reg *Registry) error {
	if m == nil {
		return errors.New("manifest is nil")
	}
	if strings.TrimSpace(m.APIVersion) == "" {
		return errors.New("apiVersion is required")
	}
	if strings.TrimSpace(m.Kind) == "" {
		return errors.New("kind is required")
	}
	if strings.TrimSpace(m.Kind) != "Model" {
		return errors.New("kind must be Model")
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
	if !ValidModelFormat(m.Spec.Format) {
		return errors.New("spec.format must be one of gguf, safetensors, onnx, pytorch, tensorrt, unknown")
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

// ToLocalModel builds a store row from a validated manifest.
func (m *LocalModelManifest) ToLocalModel() *LocalModel {
	if m == nil {
		return nil
	}
	row := &LocalModel{
		Name:       strings.TrimSpace(m.Metadata.Name),
		Source:     ModelSourceLocal,
		Repo:       strings.TrimSpace(m.Spec.Repo),
		Revision:   strings.TrimSpace(m.Spec.Revision),
		RegistryID: m.Spec.Registry,
		Format:     strings.ToLower(strings.TrimSpace(m.Spec.Format)),
		State:      ModelStatePending,
	}
	row.SetFiles(m.Spec.Files)
	row.NormalizeDefaults()
	return row
}

// FilesJSON returns the spec.files list encoded for SQLite.
func (m *LocalModelManifest) FilesJSON() string {
	if m == nil || m.Spec.Files == nil {
		return "[]"
	}
	raw, err := json.Marshal(m.Spec.Files)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

func validateModelRepo(repo string) error {
	trimmed := strings.TrimSpace(repo)
	if trimmed == "" {
		return errors.New("spec.repo is required")
	}
	if strings.Contains(trimmed, "://") {
		return errors.New("spec.repo must not include a URL scheme or host")
	}
	if parsed, err := url.Parse(trimmed); err == nil && parsed.Scheme != "" {
		return errors.New("spec.repo must not include a URL scheme or host")
	}
	if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "//") {
		return errors.New("spec.repo must not include a URL scheme or host")
	}
	first, _, _ := strings.Cut(trimmed, "/")
	if first == "" || strings.Contains(first, ".") || strings.Contains(first, ":") || strings.EqualFold(first, "localhost") {
		return errors.New("spec.repo must not include a URL scheme or host")
	}
	return nil
}
