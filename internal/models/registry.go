package models

import (
	"errors"
	"fmt"
	"strings"
)

// RequireOCIForImagePull returns an error when a registry cannot be used to pull
// container images. A nil registry is allowed (unauthenticated default pull).
func RequireOCIForImagePull(reg *Registry) error {
	if reg == nil {
		return nil
	}
	reg.NormalizeDefaults()
	if reg.NormalizedType() == RegistryTypeOCI {
		return nil
	}
	return fmt.Errorf("registry %d has type %q; container image pull requires type %s",
		reg.ID, reg.NormalizedType(), RegistryTypeOCI)
}

// RequireRegistryType returns an error when the registry type does not match want.
func RequireRegistryType(reg *Registry, want string) error {
	if reg == nil {
		return errors.New("registry is required")
	}
	reg.NormalizeDefaults()
	got := reg.NormalizedType()
	want = NormalizeRegistryType(want)
	if got != want {
		return fmt.Errorf("registry %d has type %q; this pull requires type %s", reg.ID, got, want)
	}
	return nil
}

// Registry types. Default is oci when omitted.
const (
	RegistryTypeOCI = "oci"
	RegistryTypeHF  = "hf"
)

// Built-in local registry row ids. These rows are seeded on startup and
// cannot be edited or removed.
const (
	BuiltInRegistryDockerIO       = 1
	BuiltInRegistryFromCache      = 2
	BuiltInRegistryHuggingFace    = 3
	DefaultHuggingFaceHubURL      = "https://huggingface.co"
	highestBuiltInLocalRegistryID = BuiltInRegistryHuggingFace
)

// Registry represents Docker / Hugging Face registry configuration.
type Registry struct {
	ID        int    `json:"id" yaml:"id"`
	URL       string `json:"url" yaml:"url"`
	IsPublic  bool   `json:"isPublic" yaml:"isPublic"`
	UserName  string `json:"userName,omitempty" yaml:"userName,omitempty"`
	Password  string `json:"password,omitempty" yaml:"password,omitempty"`
	UserEmail string `json:"userEmail,omitempty" yaml:"userEmail,omitempty"`
	Type      string `json:"type,omitempty" yaml:"type,omitempty"`
	CAB64     string `json:"ca,omitempty" yaml:"ca,omitempty"`
	Insecure  bool   `json:"insecure,omitempty" yaml:"insecure,omitempty"`
}

// NewRegistry creates a public or credentialed OCI registry. Type is oci,
// extra CA is empty, and insecure is false. Use RegistryBuilder when type,
// ca, or insecure must be set.
func NewRegistry(id int, url string, isPublic bool, userName, password, userEmail string) *Registry {
	reg := &Registry{
		ID:        id,
		URL:       url,
		IsPublic:  isPublic,
		UserName:  userName,
		Password:  password,
		UserEmail: userEmail,
		Type:      RegistryTypeOCI,
	}
	reg.NormalizeDefaults()
	return reg
}

// IsBuiltInLocalRegistryID reports whether id is a seeded local default
// (docker.io, from_cache, or Hugging Face Hub).
func IsBuiltInLocalRegistryID(id int) bool {
	return id == BuiltInRegistryDockerIO ||
		id == BuiltInRegistryFromCache ||
		id == BuiltInRegistryHuggingFace
}

// HighestBuiltInLocalRegistryID is the largest reserved local registry id.
func HighestBuiltInLocalRegistryID() int {
	return highestBuiltInLocalRegistryID
}

// BuiltInLocalRegistries returns the seeded local rows: public docker.io
// (oci), from_cache (oci), and public Hugging Face Hub (hf).
func BuiltInLocalRegistries() []*Registry {
	return []*Registry{
		NewRegistry(BuiltInRegistryDockerIO, "docker.io", true, "", "", ""),
		NewRegistry(BuiltInRegistryFromCache, "from_cache", true, "", "", ""),
		NewRegistryBuilder().
			SetID(BuiltInRegistryHuggingFace).
			SetURL(DefaultHuggingFaceHubURL).
			SetIsPublic(true).
			SetType(RegistryTypeHF).
			Build(),
	}
}

// BuiltInControllerRegistries returns controller-table defaults (docker.io
// and from_cache). Hugging Face Hub is local-only.
func BuiltInControllerRegistries() []*Registry {
	out := make([]*Registry, 0, 2)
	for _, reg := range BuiltInLocalRegistries() {
		if reg.ID == BuiltInRegistryHuggingFace {
			continue
		}
		out = append(out, reg)
	}
	return out
}

// NormalizeDefaults applies Registry type default (oci) and trims identity fields.
func (r *Registry) NormalizeDefaults() {
	if r == nil {
		return
	}
	r.Type = NormalizeRegistryType(r.Type)
	r.URL = strings.TrimSpace(r.URL)
	r.CAB64 = strings.TrimSpace(r.CAB64)
}

// NormalizedType returns the effective registry type (oci when empty).
func (r *Registry) NormalizedType() string {
	if r == nil {
		return RegistryTypeOCI
	}
	return NormalizeRegistryType(r.Type)
}

// NormalizeRegistryType returns oci when type is empty; otherwise lower-trimmed type.
func NormalizeRegistryType(registryType string) string {
	trimmed := strings.ToLower(strings.TrimSpace(registryType))
	if trimmed == "" {
		return RegistryTypeOCI
	}
	return trimmed
}

// ValidRegistryType reports whether type is oci or hf (empty is treated as oci).
func ValidRegistryType(registryType string) bool {
	switch NormalizeRegistryType(registryType) {
	case RegistryTypeOCI, RegistryTypeHF:
		return true
	default:
		return false
	}
}

// RegistryIdentityCollision returns an error when the same id already exists with a
// different (type, url) pair.
func RegistryIdentityCollision(existing, incoming *Registry) error {
	if existing == nil || incoming == nil {
		return nil
	}
	if existing.ID != incoming.ID {
		return nil
	}
	existing.NormalizeDefaults()
	incoming.NormalizeDefaults()
	if existing.Type != incoming.Type || existing.URL != incoming.URL {
		return fmt.Errorf("registry id %d collision: existing type=%q url=%q differs from type=%q url=%q",
			existing.ID, existing.Type, existing.URL, incoming.Type, incoming.URL)
	}
	return nil
}

// Equals checks if two Registries are equal
func (r *Registry) Equals(other *Registry) bool {
	if other == nil {
		return false
	}
	return r.ID == other.ID && r.IsPublic == other.IsPublic
}

// RegistryBuilder is a builder for creating Registry instances
type RegistryBuilder struct {
	id        int
	url       string
	isPublic  bool
	userName  string
	password  string
	userEmail string
	regType   string
	caB64     string
	insecure  bool
}

// NewRegistryBuilder creates a new RegistryBuilder
func NewRegistryBuilder() *RegistryBuilder {
	return &RegistryBuilder{}
}

// SetID sets the registry ID
func (b *RegistryBuilder) SetID(id int) *RegistryBuilder {
	b.id = id
	return b
}

// SetURL sets the registry URL
func (b *RegistryBuilder) SetURL(url string) *RegistryBuilder {
	b.url = url
	return b
}

// SetIsPublic sets whether the registry is public
func (b *RegistryBuilder) SetIsPublic(isPublic bool) *RegistryBuilder {
	b.isPublic = isPublic
	return b
}

// SetUserName sets the registry username
func (b *RegistryBuilder) SetUserName(userName string) *RegistryBuilder {
	b.userName = userName
	return b
}

// SetPassword sets the registry password
func (b *RegistryBuilder) SetPassword(password string) *RegistryBuilder {
	b.password = password
	return b
}

// SetUserEmail sets the registry user email
func (b *RegistryBuilder) SetUserEmail(userEmail string) *RegistryBuilder {
	b.userEmail = userEmail
	return b
}

// SetType sets the registry type (oci or hf).
func (b *RegistryBuilder) SetType(registryType string) *RegistryBuilder {
	b.regType = registryType
	return b
}

// SetCAB64 sets the optional base64 PEM CA bundle.
func (b *RegistryBuilder) SetCAB64(caB64 string) *RegistryBuilder {
	b.caB64 = caB64
	return b
}

// SetInsecure sets whether HTTP / skipped TLS verify is allowed.
func (b *RegistryBuilder) SetInsecure(insecure bool) *RegistryBuilder {
	b.insecure = insecure
	return b
}

// Build creates a Registry from the builder
func (b *RegistryBuilder) Build() *Registry {
	reg := NewRegistry(b.id, b.url, b.isPublic, b.userName, b.password, b.userEmail)
	if strings.TrimSpace(b.regType) != "" {
		reg.Type = b.regType
	}
	reg.CAB64 = b.caB64
	reg.Insecure = b.insecure
	reg.NormalizeDefaults()
	return reg
}
