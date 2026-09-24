package fieldagent

import (
	"encoding/json"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/containerapply"
	"github.com/eclipse-iofog/edgelet/internal/models"
)

// replaceLatestMicroservices stores the controller list and returns UUIDs whose
// desired spec changed. Comparison is by value, so a reload of the same spec
// does not mark the fleet.
func (fa *FieldAgent) replaceLatestMicroservices(microservices []*models.Microservice) []string {
	if fa == nil {
		return nil
	}
	next := make(map[string]string, len(microservices))
	order := make([]string, 0, len(microservices))
	for _, ms := range microservices {
		if ms == nil {
			continue
		}
		uuid := strings.TrimSpace(ms.MicroserviceUUID)
		if uuid == "" {
			continue
		}
		if _, seen := next[uuid]; seen {
			continue
		}
		next[uuid] = desiredSignature(ms)
		order = append(order, uuid)
	}

	fa.microservicesMu.Lock()
	prev := fa.latestDesired
	fa.latestDesired = next
	fa.latestMicroservices = make([]*models.Microservice, len(microservices))
	copy(fa.latestMicroservices, microservices)
	fa.microservicesMu.Unlock()

	marked := make([]string, 0)
	for _, uuid := range order {
		sig := next[uuid]
		old, ok := prev[uuid]
		if !ok || old != sig || desiredNeedsWake(sig) {
			marked = append(marked, uuid)
		}
	}
	for uuid := range prev {
		if _, ok := next[uuid]; !ok {
			marked = append(marked, uuid)
		}
	}
	return marked
}

// desiredNeedsWake reports a delete or rebuild that must be reconciled again
// even when the rest of the spec is unchanged.
func desiredNeedsWake(sig string) bool {
	var view desiredView
	if err := json.Unmarshal([]byte(sig), &view); err != nil {
		return true
	}
	return view.Delete || view.Rebuild
}

type desiredView struct {
	Image             string   `json:"image,omitempty"`
	RegistryID        int      `json:"registryId,omitempty"`
	HostNetwork       bool     `json:"hostNetwork,omitempty"`
	Env               string   `json:"env,omitempty"`
	Ports             string   `json:"ports,omitempty"`
	Fingerprint       string   `json:"fingerprint,omitempty"`
	ModelItems        []string `json:"modelItems,omitempty"`
	KnowledgeItems    []string `json:"knowledgeItems,omitempty"`
	Delete            bool     `json:"delete,omitempty"`
	DeleteWithCleanup bool     `json:"deleteWithCleanup,omitempty"`
	Rebuild           bool     `json:"rebuild,omitempty"`
}

func desiredSignature(ms *models.Microservice) string {
	if ms == nil {
		return ""
	}
	fp, err := containerapply.Marshal(containerapply.FromMicroservice(ms))
	if err != nil {
		fp = ""
	}
	view := desiredView{
		Image:             strings.TrimSpace(ms.ImageName),
		RegistryID:        ms.RegistryID,
		HostNetwork:       ms.HostNetworkMode,
		Env:               jsonCompact(ms.EnvVars),
		Ports:             jsonCompact(ms.PortMappings),
		Fingerprint:       fp,
		ModelItems:        models.CatalogItemNames(ms.Models),
		KnowledgeItems:    models.KnowledgeCatalogItemNames(ms.Knowledge),
		Delete:            ms.Delete,
		DeleteWithCleanup: ms.DeleteWithCleanup,
		Rebuild:           ms.Rebuild,
	}
	raw, err := json.Marshal(view)
	if err != nil {
		return ms.MicroserviceUUID
	}
	return string(raw)
}

func jsonCompact(v any) string {
	if v == nil {
		return ""
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	if string(raw) == "null" || string(raw) == "[]" {
		return ""
	}
	return string(raw)
}
