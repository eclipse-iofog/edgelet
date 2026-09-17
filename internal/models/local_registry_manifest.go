//revive:disable:nested-structs
package models

import (
	"encoding/base64"
	"errors"
	"strings"
	"unicode"
)

// LocalRegistrySpec is the spec block of a local Registry deploy document.
type LocalRegistrySpec struct {
	ID        int    `yaml:"id,omitempty" json:"id,omitempty"`
	Type      string `yaml:"type,omitempty" json:"type,omitempty"`
	URL       string `yaml:"url" json:"url"`
	UserName  string `yaml:"username,omitempty" json:"username,omitempty"`
	Password  string `yaml:"password,omitempty" json:"password,omitempty"`
	UserEmail string `yaml:"email,omitempty" json:"email,omitempty"`
	Private   bool   `yaml:"private" json:"private"`
	CA        string `yaml:"ca,omitempty" json:"ca,omitempty"`
	Insecure  bool   `yaml:"insecure" json:"insecure"`
}

// LocalRegistryManifest represents local registry deployment YAML.
type LocalRegistryManifest struct {
	APIVersion string            `yaml:"apiVersion" json:"apiVersion"`
	Kind       string            `yaml:"kind" json:"kind"`
	Spec       LocalRegistrySpec `yaml:"spec" json:"spec"`
}

func (m *LocalRegistryManifest) Validate() error {
	if m == nil {
		return errors.New("manifest is nil")
	}
	if strings.TrimSpace(m.APIVersion) == "" {
		return errors.New("apiVersion is required")
	}
	if strings.TrimSpace(m.Kind) == "" {
		return errors.New("kind is required")
	}
	if strings.TrimSpace(m.Kind) != "Registry" {
		return errors.New("kind must be Registry")
	}
	switch strings.TrimSpace(m.APIVersion) {
	case "edgelet.iofog.org/v1":
	default:
		return errors.New("apiVersion must be edgelet.iofog.org/v1")
	}
	if strings.TrimSpace(m.Spec.URL) == "" {
		return errors.New("spec.url is required")
	}
	if m.Spec.ID < 0 {
		return errors.New("spec.id must be a positive registry id")
	}

	m.Spec.Type = NormalizeRegistryType(m.Spec.Type)
	if !ValidRegistryType(m.Spec.Type) {
		return errors.New("spec.type must be oci or hf")
	}

	if ca := strings.TrimSpace(m.Spec.CA); ca != "" {
		if err := validateRegistryCAB64(ca); err != nil {
			return err
		}
		m.Spec.CA = strings.TrimSpace(m.Spec.CA)
	}

	if m.Spec.Type == RegistryTypeHF && strings.TrimSpace(m.Spec.UserEmail) != "" {
		return errors.New("spec.email is only valid when spec.type is oci")
	}

	if m.Spec.Private {
		if strings.TrimSpace(m.Spec.Password) == "" {
			return errors.New("spec.password is required when spec.private=true")
		}
		if m.Spec.Type == RegistryTypeOCI && strings.TrimSpace(m.Spec.UserName) == "" {
			return errors.New("spec.username is required when spec.private=true")
		}
	}
	return nil
}

// ToRegistry builds a store Registry from the validated manifest and allocated/upsert id.
func (m *LocalRegistryManifest) ToRegistry(id int) *Registry {
	if m == nil {
		return nil
	}
	reg := &Registry{
		ID:        id,
		URL:       strings.TrimSpace(m.Spec.URL),
		IsPublic:  !m.Spec.Private,
		UserName:  m.Spec.UserName,
		Password:  m.Spec.Password,
		UserEmail: m.Spec.UserEmail,
		Type:      NormalizeRegistryType(m.Spec.Type),
		CAB64:     strings.TrimSpace(m.Spec.CA),
		Insecure:  m.Spec.Insecure,
	}
	reg.NormalizeDefaults()
	return reg
}

// CollisionWith reports a validate error when existing has a different (type, url).
func (m *LocalRegistryManifest) CollisionWith(existing *Registry) error {
	if m == nil || existing == nil {
		return nil
	}
	id := m.Spec.ID
	if id <= 0 {
		id = existing.ID
	}
	return RegistryIdentityCollision(existing, m.ToRegistry(id))
}

func validateRegistryCAB64(ca string) error {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, ca)
	if _, err := base64.StdEncoding.DecodeString(cleaned); err != nil {
		if _, rawErr := base64.RawStdEncoding.DecodeString(cleaned); rawErr != nil {
			return errors.New("spec.ca must be base64-encoded PEM")
		}
	}
	return nil
}
