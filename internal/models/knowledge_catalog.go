package models

import (
	"path"
	"strings"
)

// Knowledge provenance on the node.
const (
	KnowledgeSourceLocal   = "local"
	KnowledgeSourceManaged = "managed"
)

// Knowledge catalog mount mode. Default is read-only.
const (
	KnowledgeCatalogPermRO = "ro"
	KnowledgeCatalogPermRW = "rw"
)

// KnowledgeCatalog is the microservice knowledge catalog bind (bindPath + items by name).
type KnowledgeCatalog struct {
	BindPath    string                 `json:"bindPath,omitempty" yaml:"bindPath,omitempty"`
	Permissions string                 `json:"permissions,omitempty" yaml:"permissions,omitempty"`
	Items       []KnowledgeCatalogItem `json:"items,omitempty" yaml:"items,omitempty"`
}

// KnowledgeCatalogItem names one Knowledge whose Ready content/ is projected under bindPath.
type KnowledgeCatalogItem struct {
	Name string `json:"name" yaml:"name"`
}

// ValidKnowledgeSource reports whether source is local or managed.
func ValidKnowledgeSource(source string) bool {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case KnowledgeSourceLocal, KnowledgeSourceManaged:
		return true
	default:
		return false
	}
}

// HasItems reports whether the knowledge catalog lists at least one item.
func (c *KnowledgeCatalog) HasItems() bool {
	return c != nil && len(c.Items) > 0
}

// ItemContainerPath is the in-container path for one knowledge catalog item: {bindPath}/{name}.
func (c *KnowledgeCatalog) ItemContainerPath(name string) string {
	if c == nil {
		return ""
	}
	return path.Join(cleanContainerPath(c.BindPath), strings.TrimSpace(name))
}

// Clone returns a shallow copy of the knowledge catalog (nil-safe).
func (c *KnowledgeCatalog) Clone() *KnowledgeCatalog {
	if c == nil {
		return nil
	}
	out := *c
	if c.Items != nil {
		out.Items = append([]KnowledgeCatalogItem(nil), c.Items...)
	}
	return &out
}

// NormalizeDefaults trims fields and defaults permissions to ro when items are present.
func (c *KnowledgeCatalog) NormalizeDefaults() {
	if c == nil {
		return
	}
	c.BindPath = strings.TrimSpace(c.BindPath)
	c.Permissions = strings.ToLower(strings.TrimSpace(c.Permissions))
	for i := range c.Items {
		c.Items[i].Name = strings.TrimSpace(c.Items[i].Name)
	}
	if c.HasItems() && c.Permissions == "" {
		c.Permissions = KnowledgeCatalogPermRO
	}
}
