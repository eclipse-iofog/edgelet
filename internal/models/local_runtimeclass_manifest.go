//revive:disable:nested-structs
package models

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// LocalRuntimeClassManifest represents local RuntimeClass deployment YAML.
// RuntimeClass uses top-level fields to keep local manifests concise.
type LocalRuntimeClassManifest struct {
	APIVersion string `yaml:"apiVersion" json:"apiVersion"`
	Kind       string `yaml:"kind" json:"kind"`
	Metadata   struct {
		Name string `yaml:"name" json:"name"`
	} `yaml:"metadata" json:"metadata"`
	Handler string `yaml:"handler" json:"handler"`
}

var runtimeClassNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

var reservedRuntimeClassNames = map[string]struct{}{
	"crun": {},
}

// IsReservedRuntimeClassName reports whether name is reserved and cannot be
// applied or deleted as a RuntimeClass (for example the default crun handler).
func IsReservedRuntimeClassName(name string) bool {
	_, reserved := reservedRuntimeClassNames[strings.TrimSpace(strings.ToLower(name))]
	return reserved
}

func (m *LocalRuntimeClassManifest) Validate() error {
	if m == nil {
		return errors.New("manifest is nil")
	}
	if strings.TrimSpace(m.APIVersion) == "" {
		return errors.New("apiVersion is required")
	}
	if strings.TrimSpace(m.Kind) == "" {
		return errors.New("kind is required")
	}
	if strings.TrimSpace(m.Kind) != "RuntimeClass" {
		return errors.New("kind must be RuntimeClass")
	}
	switch strings.TrimSpace(m.APIVersion) {
	case "edgelet.iofog.org/v1":
	default:
		return errors.New("apiVersion must be edgelet.iofog.org/v1")
	}

	name := strings.TrimSpace(strings.ToLower(m.Metadata.Name))
	if name == "" {
		return errors.New("metadata.name is required")
	}
	if len(name) > 63 || !runtimeClassNamePattern.MatchString(name) {
		return errors.New("metadata.name must be <= 63 characters and follow DNS-1123 label format")
	}
	if _, reserved := reservedRuntimeClassNames[name]; reserved {
		return fmt.Errorf("metadata.name %q is reserved", name)
	}
	if strings.HasSuffix(name, "-local") {
		return errors.New("metadata.name must not end with -local")
	}

	handler := strings.TrimSpace(strings.ToLower(m.Handler))
	if handler == "" {
		return errors.New("handler is required")
	}
	if len(handler) > 63 || !runtimeClassNamePattern.MatchString(handler) {
		return errors.New("handler must be <= 63 characters and follow DNS-1123 label format")
	}
	if _, reserved := reservedRuntimeClassNames[handler]; reserved {
		return fmt.Errorf("handler %q is reserved", handler)
	}

	m.Metadata.Name = name
	m.Handler = handler
	return nil
}
