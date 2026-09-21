package models

import (
	"fmt"
	"strings"
)

// KnowledgeCatalogWaitingMessage is the status text when a workload is waiting
// for Knowledge download and no named item is available yet.
const KnowledgeCatalogWaitingMessage = "workload is waiting for knowledge download"

// KnowledgeStatusInfo is the stored identity and lifecycle of one named Knowledge.
type KnowledgeStatusInfo struct {
	Source      string
	State       string
	Exists      bool
	Generation  int64
	ContentPath string
	LastError   string
}

// KnowledgeStatusLookup returns stored status for a Knowledge name.
// Exists is false when the name is not present.
type KnowledgeStatusLookup func(name string) (KnowledgeStatusInfo, error)

// ErrKnowledgeFailed is returned when a knowledge catalog item names a Failed Knowledge.
type ErrKnowledgeFailed struct {
	Name   string
	Detail string
}

func (e *ErrKnowledgeFailed) Error() string {
	if e == nil {
		return "catalog knowledge failed"
	}
	if strings.TrimSpace(e.Detail) == "" {
		return fmt.Sprintf("catalog knowledge item %q is Failed", e.Name)
	}
	return fmt.Sprintf("catalog knowledge item %q is Failed: %s", e.Name, e.Detail)
}

// ErrKnowledgeSourceScope is returned when a knowledge catalog item name is missing
// or not allowed for this microservice (local workloads bind local Knowledge only;
// controller workloads bind managed Knowledge only).
type ErrKnowledgeSourceScope struct {
	Name    string
	Want    string
	Have    string
	Missing bool
}

func (e *ErrKnowledgeSourceScope) Error() string {
	if e == nil {
		return "knowledge source scope error"
	}
	if e.Missing {
		return fmt.Sprintf("catalog knowledge item %q is not a %s Knowledge", e.Name, e.Want)
	}
	return fmt.Sprintf("catalog knowledge item %q has source %q; this microservice may bind %s Knowledge only", e.Name, e.Have, e.Want)
}

// ValidateKnowledgeCatalogApply checks source scope plus Failed/missing names for apply-time reject.
// Pending and Pulling names are allowed so the workload can wait for download.
func ValidateKnowledgeCatalogApply(c *KnowledgeCatalog, requiredSource string, lookup KnowledgeStatusLookup) error {
	if c == nil || !c.HasItems() {
		return nil
	}
	want := strings.ToLower(strings.TrimSpace(requiredSource))
	if !ValidKnowledgeSource(want) {
		return fmt.Errorf("invalid required knowledge source %q", requiredSource)
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
			return &ErrKnowledgeSourceScope{Name: name, Want: want, Missing: true}
		}
		have := strings.ToLower(strings.TrimSpace(info.Source))
		if have != want {
			return &ErrKnowledgeSourceScope{Name: name, Want: want, Have: have}
		}
		if strings.TrimSpace(info.State) == KnowledgeStateFailed {
			return &ErrKnowledgeFailed{Name: name, Detail: strings.TrimSpace(info.LastError)}
		}
	}
	return nil
}

func formatKnowledgeCatalogWaitMessage(waiting []catalogWaitEntry) string {
	if len(waiting) == 0 {
		return KnowledgeCatalogWaitingMessage
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
		return KnowledgeCatalogWaitingMessage
	}
	return "waiting for knowledge download: " + strings.Join(parts, ", ")
}

// EvaluateKnowledgeCatalogStartGate decides create vs wait vs fail from named Knowledge status.
func EvaluateKnowledgeCatalogStartGate(c *KnowledgeCatalog, requiredSource string, lookup KnowledgeStatusLookup) (CatalogGateDecision, string, error) {
	if c == nil || !c.HasItems() {
		return CatalogGateAllow, "", nil
	}
	want := strings.ToLower(strings.TrimSpace(requiredSource))
	if !ValidKnowledgeSource(want) {
		return CatalogGateFail, fmt.Sprintf("invalid required knowledge source %q", requiredSource), nil
	}
	if lookup == nil {
		return CatalogGateFail, "knowledge status lookup is not configured", nil
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
			msg := (&ErrKnowledgeSourceScope{Name: name, Want: want, Missing: true}).Error()
			return CatalogGateFail, msg, nil
		}
		have := strings.ToLower(strings.TrimSpace(info.Source))
		if have != want {
			msg := (&ErrKnowledgeSourceScope{Name: name, Want: want, Have: have}).Error()
			return CatalogGateFail, msg, nil
		}
		switch strings.TrimSpace(info.State) {
		case KnowledgeStateReady:
			continue
		case KnowledgeStatePending, KnowledgeStatePulling:
			waiting = append(waiting, catalogWaitEntry{Name: name, State: strings.TrimSpace(info.State)})
		case KnowledgeStateFailed:
			msg := (&ErrKnowledgeFailed{Name: name, Detail: strings.TrimSpace(info.LastError)}).Error()
			return CatalogGateFail, msg, nil
		default:
			msg := fmt.Sprintf("catalog knowledge item %q has unknown state %q", name, info.State)
			return CatalogGateFail, msg, nil
		}
	}
	if len(waiting) > 0 {
		return CatalogGateWait, formatKnowledgeCatalogWaitMessage(waiting), nil
	}
	return CatalogGateAllow, "", nil
}

// KnowledgeCatalogMountNeedsRecreate reports bindPath or catalog permissions drift.
// Item add/remove and Knowledge generation changes do not recreate the container.
func KnowledgeCatalogMountNeedsRecreate(prev, next *KnowledgeCatalog) bool {
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

// CombineCatalogStartGates merges model and knowledge start-gate outcomes.
// Fail wins, then wait. The workload starts only when both catalogs allow.
func CombineCatalogStartGates(modelDec CatalogGateDecision, modelMsg string, knowledgeDec CatalogGateDecision, knowledgeMsg string) (CatalogGateDecision, string) {
	if modelDec == CatalogGateFail {
		return CatalogGateFail, strings.TrimSpace(modelMsg)
	}
	if knowledgeDec == CatalogGateFail {
		return CatalogGateFail, strings.TrimSpace(knowledgeMsg)
	}
	if modelDec == CatalogGateWait || knowledgeDec == CatalogGateWait {
		return CatalogGateWait, joinCatalogWaitMessages(modelMsg, knowledgeMsg)
	}
	return CatalogGateAllow, ""
}

func joinCatalogWaitMessages(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return CatalogWaitingMessage
	}
	return strings.Join(out, "; ")
}

// KnowledgeCatalogItemNames returns trimmed item names in catalog order.
func KnowledgeCatalogItemNames(c *KnowledgeCatalog) []string {
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
