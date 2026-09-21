package models

import (
	"encoding/json"
	"strings"
)

// MarshalModelsJSON encodes the catalog bind for the models column. Empty catalog becomes {}.
func (m *Microservice) MarshalModelsJSON() (string, error) {
	if m == nil || m.Models == nil {
		return "{}", nil
	}
	raw, err := json.Marshal(m.Models)
	if err != nil {
		return "", err
	}
	if string(raw) == "null" {
		return "{}", nil
	}
	return string(raw), nil
}

// UnmarshalModelsJSON loads the catalog bind from the models column.
func (m *Microservice) UnmarshalModelsJSON(raw string) error {
	if m == nil {
		return nil
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		m.Models = nil
		return nil
	}
	cat := &ModelCatalog{}
	if err := json.Unmarshal([]byte(trimmed), cat); err != nil {
		return err
	}
	cat.NormalizeDefaults()
	m.Models = cat
	return nil
}

// MarshalKnowledgeJSON encodes the knowledge catalog bind for the knowledge column. Empty catalog becomes {}.
func (m *Microservice) MarshalKnowledgeJSON() (string, error) {
	if m == nil || m.Knowledge == nil {
		return "{}", nil
	}
	raw, err := json.Marshal(m.Knowledge)
	if err != nil {
		return "", err
	}
	if string(raw) == "null" {
		return "{}", nil
	}
	return string(raw), nil
}

// UnmarshalKnowledgeJSON loads the knowledge catalog bind from the knowledge column.
func (m *Microservice) UnmarshalKnowledgeJSON(raw string) error {
	if m == nil {
		return nil
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		m.Knowledge = nil
		return nil
	}
	cat := &KnowledgeCatalog{}
	if err := json.Unmarshal([]byte(trimmed), cat); err != nil {
		return err
	}
	cat.NormalizeDefaults()
	m.Knowledge = cat
	return nil
}
