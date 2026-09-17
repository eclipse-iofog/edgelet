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
