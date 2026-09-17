package models

import "strings"

// ControllerRuntimeClass is a fleet-desired RuntimeClass row (name + handler).
type ControllerRuntimeClass struct {
	Name    string `json:"name"`
	Handler string `json:"handler"`
}

// NormalizeDefaults trims and lowercases fleet RuntimeClass identity fields.
func (r *ControllerRuntimeClass) NormalizeDefaults() {
	if r == nil {
		return
	}
	r.Name = strings.TrimSpace(strings.ToLower(r.Name))
	r.Handler = strings.TrimSpace(strings.ToLower(r.Handler))
}
