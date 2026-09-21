package runtimeapi

import (
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"gopkg.in/yaml.v3"
)

func catalogsFromLocalManifestYAML(raw string) (*models.ModelCatalog, *models.KnowledgeCatalog) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	doc := &models.LocalDeployManifest{}
	if err := yaml.Unmarshal([]byte(raw), doc); err != nil {
		return nil, nil
	}
	if doc.Spec.Models != nil {
		doc.Spec.Models.NormalizeDefaults()
	}
	if doc.Spec.Knowledge != nil {
		doc.Spec.Knowledge.NormalizeDefaults()
	}
	return doc.Spec.Models, doc.Spec.Knowledge
}

func catalogAPIMap(c *models.ModelCatalog) map[string]any {
	if c == nil || !c.HasItems() {
		return nil
	}
	clone := c.Clone()
	clone.NormalizeDefaults()
	items := make([]map[string]any, 0, len(clone.Items))
	for _, item := range clone.Items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		items = append(items, map[string]any{"name": name})
	}
	if len(items) == 0 {
		return nil
	}
	return map[string]any{
		"bindPath":    clone.BindPath,
		"permissions": clone.Permissions,
		"items":       items,
	}
}

func knowledgeCatalogAPIMap(c *models.KnowledgeCatalog) map[string]any {
	if c == nil || !c.HasItems() {
		return nil
	}
	clone := c.Clone()
	clone.NormalizeDefaults()
	items := make([]map[string]any, 0, len(clone.Items))
	for _, item := range clone.Items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		items = append(items, map[string]any{"name": name})
	}
	if len(items) == 0 {
		return nil
	}
	return map[string]any{
		"bindPath":    clone.BindPath,
		"permissions": clone.Permissions,
		"items":       items,
	}
}

func attachWorkloadCatalogInspect(
	item map[string]any,
	catalog *models.ModelCatalog,
	knowledge *models.KnowledgeCatalog,
	requiredSource string,
	modelLookup models.ModelStatusLookup,
	knowledgeLookup models.KnowledgeStatusLookup,
	storedStatus string,
) {
	if item == nil {
		return
	}
	storedStatus = strings.TrimSpace(storedStatus)
	if api := catalogAPIMap(catalog); api != nil {
		item["models"] = api
	}
	if api := knowledgeCatalogAPIMap(knowledge); api != nil {
		item["knowledge"] = api
	}

	modelDec, modelMsg := models.CatalogGateAllow, ""
	if catalog != nil && catalog.HasItems() && modelLookup != nil {
		dec, msg, err := models.EvaluateCatalogStartGate(catalog, requiredSource, modelLookup)
		if err == nil {
			modelDec, modelMsg = dec, msg
		}
	}
	knowledgeDec, knowledgeMsg := models.CatalogGateAllow, ""
	if knowledge != nil && knowledge.HasItems() && knowledgeLookup != nil {
		dec, msg, err := models.EvaluateKnowledgeCatalogStartGate(knowledge, requiredSource, knowledgeLookup)
		if err == nil {
			knowledgeDec, knowledgeMsg = dec, msg
		}
	}
	dec, msg := models.CombineCatalogStartGates(modelDec, modelMsg, knowledgeDec, knowledgeMsg)
	if strings.TrimSpace(msg) != "" && (dec == models.CatalogGateWait || dec == models.CatalogGateFail) {
		item["statusText"] = msg
		return
	}
	if storedStatus != "" {
		item["statusText"] = storedStatus
	}
}

func lookupReporterMicroserviceStatus(sr *statusreporter.StatusReporter, uuid string) *models.MicroserviceStatus {
	if sr == nil {
		return nil
	}
	pmStatus := sr.GetProcessManagerStatus()
	if pmStatus == nil {
		return nil
	}
	return pmStatus.LookupMicroserviceStatus(uuid)
}

func attachDurabilityInspect(item map[string]any, status *models.MicroserviceStatus, fallbackLastError string, fallbackRestartCount int) {
	if item == nil {
		return
	}
	lastError := strings.TrimSpace(fallbackLastError)
	restartCount := fallbackRestartCount
	if status != nil {
		if status.ErrorMessage != nil {
			item["errorMessage"] = status.ErrorMessage
		}
		if trimmed := strings.TrimSpace(status.LastError); trimmed != "" {
			lastError = trimmed
		}
		if status.LastErrorAt != 0 {
			item["lastErrorAt"] = status.LastErrorAt
		}
		if status.RestartCount > 0 {
			restartCount = status.RestartCount
		}
	}
	if lastError != "" {
		item["lastError"] = lastError
	} else {
		delete(item, "lastError")
	}
	if restartCount > 0 {
		item["restartCount"] = restartCount
	} else {
		delete(item, "restartCount")
	}
}
