package models

import (
	"fmt"
	"strings"
)

// ErrModelFailed is returned when a catalog item names a Failed model.
type ErrModelFailed struct {
	Name   string
	Detail string
}

func (e *ErrModelFailed) Error() string {
	if e == nil {
		return "catalog model failed"
	}
	if strings.TrimSpace(e.Detail) == "" {
		return fmt.Sprintf("catalog item %q is Failed", e.Name)
	}
	return fmt.Sprintf("catalog item %q is Failed: %s", e.Name, e.Detail)
}

// ValidateCatalogApply checks source scope plus Failed/missing names for apply-time reject.
// Pending and Pulling names are allowed so the workload can wait for download.
func ValidateCatalogApply(c *ModelCatalog, requiredSource string, lookup ModelStatusLookup) error {
	if c == nil || !c.HasItems() {
		return nil
	}
	want := strings.ToLower(strings.TrimSpace(requiredSource))
	if !ValidModelSource(want) {
		return fmt.Errorf("invalid required model source %q", requiredSource)
	}
	if lookup == nil {
		return nil
	}
	for _, item := range c.Items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		info, err := lookup(name)
		if err != nil {
			return err
		}
		if !info.Exists {
			return &ErrModelSourceScope{Name: name, Want: want, Missing: true}
		}
		have := strings.ToLower(strings.TrimSpace(info.Source))
		if have != want {
			return &ErrModelSourceScope{Name: name, Want: want, Have: have}
		}
		if strings.TrimSpace(info.State) == ModelStateFailed {
			return &ErrModelFailed{Name: name, Detail: strings.TrimSpace(info.LastError)}
		}
	}
	return nil
}

// catalogWaitEntry is one named model that is still downloading.
type catalogWaitEntry struct {
	Name  string
	State string
}

// formatCatalogWaitMessage is the QUEUED status text for models that are not Ready yet.
func formatCatalogWaitMessage(waiting []catalogWaitEntry) string {
	if len(waiting) == 0 {
		return CatalogWaitingMessage
	}
	parts := make([]string, 0, len(waiting))
	for _, item := range waiting {
		name := strings.TrimSpace(item.Name)
		state := strings.TrimSpace(item.State)
		if name == "" {
			continue
		}
		if state == "" {
			parts = append(parts, name)
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", name, state))
	}
	if len(parts) == 0 {
		return CatalogWaitingMessage
	}
	return "waiting for model download: " + strings.Join(parts, ", ")
}

// EvaluateCatalogStartGate decides create vs wait vs fail from named item status.
func EvaluateCatalogStartGate(c *ModelCatalog, requiredSource string, lookup ModelStatusLookup) (CatalogGateDecision, string, error) {
	if c == nil || !c.HasItems() {
		return CatalogGateAllow, "", nil
	}
	want := strings.ToLower(strings.TrimSpace(requiredSource))
	if !ValidModelSource(want) {
		return CatalogGateFail, fmt.Sprintf("invalid required model source %q", requiredSource), nil
	}
	if lookup == nil {
		return CatalogGateFail, "model status lookup is not configured", nil
	}

	waiting := make([]catalogWaitEntry, 0)
	for _, item := range c.Items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		info, err := lookup(name)
		if err != nil {
			return CatalogGateFail, "", err
		}
		if !info.Exists {
			msg := (&ErrModelSourceScope{Name: name, Want: want, Missing: true}).Error()
			return CatalogGateFail, msg, nil
		}
		have := strings.ToLower(strings.TrimSpace(info.Source))
		if have != want {
			msg := (&ErrModelSourceScope{Name: name, Want: want, Have: have}).Error()
			return CatalogGateFail, msg, nil
		}
		switch strings.TrimSpace(info.State) {
		case ModelStateReady:
			continue
		case ModelStatePending, ModelStatePulling:
			waiting = append(waiting, catalogWaitEntry{Name: name, State: strings.TrimSpace(info.State)})
		case ModelStateFailed:
			msg := (&ErrModelFailed{Name: name, Detail: strings.TrimSpace(info.LastError)}).Error()
			return CatalogGateFail, msg, nil
		default:
			msg := fmt.Sprintf("catalog item %q has unknown state %q", name, info.State)
			return CatalogGateFail, msg, nil
		}
	}
	if len(waiting) > 0 {
		return CatalogGateWait, formatCatalogWaitMessage(waiting), nil
	}
	return CatalogGateAllow, "", nil
}

// CatalogMountNeedsRecreate reports bindPath or catalog permissions drift.
// Item add/remove and model generation changes do not recreate the container.
func CatalogMountNeedsRecreate(prev, next *ModelCatalog) bool {
	if !prev.HasItems() && !next.HasItems() {
		return false
	}
	if !prev.HasItems() || !next.HasItems() {
		return prev.HasItems() != next.HasItems()
	}
	left := prev.Clone()
	right := next.Clone()
	left.NormalizeDefaults()
	right.NormalizeDefaults()
	if cleanContainerPath(left.BindPath) != cleanContainerPath(right.BindPath) {
		return true
	}
	return !strings.EqualFold(strings.TrimSpace(left.Permissions), strings.TrimSpace(right.Permissions))
}

// CatalogItemNames returns trimmed item names in catalog order.
func CatalogItemNames(c *ModelCatalog) []string {
	if c == nil || len(c.Items) == 0 {
		return nil
	}
	out := make([]string, 0, len(c.Items))
	for _, item := range c.Items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		out = append(out, name)
	}
	return out
}
