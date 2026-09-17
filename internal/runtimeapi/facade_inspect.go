package runtimeapi

import (
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"gopkg.in/yaml.v3"
)

func catalogFromLocalManifestYAML(raw string) *models.ModelCatalog {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	doc := &models.LocalDeployManifest{}
	if err := yaml.Unmarshal([]byte(raw), doc); err != nil {
		return nil
	}
	if doc.Spec.Models != nil {
		doc.Spec.Models.NormalizeDefaults()
	}
	return doc.Spec.Models
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

func attachCatalogInspect(item map[string]any, catalog *models.ModelCatalog, requiredSource string, lookup models.ModelStatusLookup, storedStatus string) {
	if item == nil {
		return
	}
	storedStatus = strings.TrimSpace(storedStatus)
	if api := catalogAPIMap(catalog); api != nil {
		item["models"] = api
	}
	if catalog != nil && catalog.HasItems() && lookup != nil {
		dec, msg, err := models.EvaluateCatalogStartGate(catalog, requiredSource, lookup)
		if err == nil && strings.TrimSpace(msg) != "" && (dec == models.CatalogGateWait || dec == models.CatalogGateFail) {
			item["statusText"] = msg
			return
		}
	}
	if storedStatus != "" {
		item["statusText"] = storedStatus
	}
}
