package models

import (
	"fmt"
	"path"
	"strings"
)

// Model provenance on the node.
const (
	ModelSourceLocal   = "local"
	ModelSourceManaged = "managed"
)

// Catalog mount mode. Default is read-only.
const (
	ModelCatalogPermRO = "ro"
	ModelCatalogPermRW = "rw"
)

// ModelCatalog is the microservice catalog bind (bindPath + items by name).
type ModelCatalog struct {
	BindPath    string             `json:"bindPath,omitempty" yaml:"bindPath,omitempty"`
	Permissions string             `json:"permissions,omitempty" yaml:"permissions,omitempty"`
	Items       []ModelCatalogItem `json:"items,omitempty" yaml:"items,omitempty"`
}

// ModelCatalogItem names one model whose Ready content/ is projected under bindPath.
type ModelCatalogItem struct {
	Name string `json:"name" yaml:"name"`
}

// ModelSourceLookup returns the stored source for a model name.
// exists is false when the name is not present.
type ModelSourceLookup func(name string) (source string, exists bool, err error)

// ModelStatusInfo is the stored identity and lifecycle of one named model.
type ModelStatusInfo struct {
	Source      string
	State       string
	Exists      bool
	Generation  int64
	ContentPath string
	LastError   string
}

// ModelStatusLookup returns stored status for a model name.
// Exists is false when the name is not present.
type ModelStatusLookup func(name string) (ModelStatusInfo, error)

// CatalogGateDecision is whether a workload may create, must wait, or has failed.
type CatalogGateDecision int

// Catalog start-gate outcomes.
const (
	CatalogGateAllow CatalogGateDecision = iota
	CatalogGateWait
	CatalogGateFail
)

// CatalogWaitingMessage is the status text when a workload is waiting for model download.
const CatalogWaitingMessage = "workload is waiting for model download"

// ErrModelSourceScope is returned when a catalog item name is missing or
// not allowed for this microservice (local workloads bind local models only;
// controller workloads bind managed models only).
type ErrModelSourceScope struct {
	Name    string
	Want    string
	Have    string
	Missing bool
}

func (e *ErrModelSourceScope) Error() string {
	if e == nil {
		return "model source scope error"
	}
	if e.Missing {
		return fmt.Sprintf("catalog item %q is not a %s model", e.Name, e.Want)
	}
	return fmt.Sprintf("catalog item %q has source %q; this microservice may bind %s models only", e.Name, e.Have, e.Want)
}

// ValidModelSource reports whether source is local or managed.
func ValidModelSource(source string) bool {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case ModelSourceLocal, ModelSourceManaged:
		return true
	default:
		return false
	}
}

// HasItems reports whether the catalog lists at least one item.
func (c *ModelCatalog) HasItems() bool {
	return c != nil && len(c.Items) > 0
}

// ItemContainerPath is the in-container path for one catalog item: {bindPath}/{name}.
func (c *ModelCatalog) ItemContainerPath(name string) string {
	if c == nil {
		return ""
	}
	return path.Join(cleanContainerPath(c.BindPath), strings.TrimSpace(name))
}

// Clone returns a shallow copy of the catalog (nil-safe).
func (c *ModelCatalog) Clone() *ModelCatalog {
	if c == nil {
		return nil
	}
	out := *c
	if c.Items != nil {
		out.Items = append([]ModelCatalogItem(nil), c.Items...)
	}
	return &out
}

// NormalizeDefaults trims fields and defaults permissions to ro when items are present.
func (c *ModelCatalog) NormalizeDefaults() {
	if c == nil {
		return
	}
	c.BindPath = strings.TrimSpace(c.BindPath)
	c.Permissions = strings.ToLower(strings.TrimSpace(c.Permissions))
	for i := range c.Items {
		c.Items[i].Name = strings.TrimSpace(c.Items[i].Name)
	}
	if c.HasItems() && c.Permissions == "" {
		c.Permissions = ModelCatalogPermRO
	}
}

func cleanContainerPath(p string) string {
	trimmed := strings.TrimSpace(p)
	if trimmed == "" {
		return ""
	}
	return path.Clean(trimmed)
}
