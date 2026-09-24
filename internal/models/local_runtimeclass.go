package models

import (
	"path/filepath"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/constants"
)

// RuntimeClass provenance on the node.
const (
	RuntimeClassSourceLocal   = "local"
	RuntimeClassSourceManaged = "managed"
)

// RuntimeClassStatus is one applied RuntimeClass on fog and local status.
type RuntimeClassStatus struct {
	Name    string `json:"name"`
	Handler string `json:"handler"`
	Source  string `json:"source"`
}

// Display renders the class as "name (handler, source)" for human status output.
func (r RuntimeClassStatus) Display() string {
	return strings.TrimSpace(r.Name) + " (" + strings.TrimSpace(r.Handler) + ", " + strings.TrimSpace(r.Source) + ")"
}

// LocalRuntimeClass is the persistent RuntimeClass row stored in SQLite.
type LocalRuntimeClass struct {
	Name        string `json:"name"`
	Handler     string `json:"handler"`
	Source      string `json:"source,omitempty"`
	RuntimeName string `json:"runtimeName,omitempty"`
	CreatedAt   int64  `json:"createdAt,omitempty"`
	UpdatedAt   int64  `json:"updatedAt,omitempty"`
}

func (r *LocalRuntimeClass) Normalize() {
	if r == nil {
		return
	}
	r.Name = strings.TrimSpace(strings.ToLower(r.Name))
	r.Handler = strings.TrimSpace(strings.ToLower(r.Handler))
	r.Source = strings.ToLower(strings.TrimSpace(r.Source))
	if r.Source == "" {
		r.Source = RuntimeClassSourceLocal
	}
	r.RuntimeName = strings.TrimSpace(strings.ToLower(r.RuntimeName))
	if r.RuntimeName == "" {
		r.RuntimeName = r.Name
	}
}

// ValidRuntimeClassSource reports whether source is local or managed.
func ValidRuntimeClassSource(source string) bool {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case RuntimeClassSourceLocal, RuntimeClassSourceManaged:
		return true
	default:
		return false
	}
}

func (r *LocalRuntimeClass) RuntimeBinaryPath() string {
	if r == nil {
		return ""
	}
	return RuntimeClassBinaryPathForHandler(r.Handler)
}

func RuntimeClassBinaryPathForHandler(handler string) string {
	normalized := strings.TrimSpace(strings.ToLower(handler))
	if normalized == "" {
		return ""
	}
	binaryName := normalized
	if !strings.HasPrefix(binaryName, "containerd-shim-") {
		binaryName = "containerd-shim-" + normalized
	}
	return filepath.Join(constants.EdgeletContainerdBinDir, binaryName)
}
