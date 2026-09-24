package fieldagent

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/store"
)

func fixtureControllerMicroservicesKnowledgeJSON() []byte {
	return []byte(`{"microservices":[{
		"uuid":"ms-knowledge-catalog",
		"imageId":"alpine:3.19",
		"knowledge":{
			"bindPath":"/knowledge",
			"permissions":"ro",
			"items":[{"name":"product-docs"}]
		}
	}]}`)
}

func TestProcessChanges_MicroserviceKnowledgeFlagLoadsMicroservices(t *testing.T) {
	var gets atomic.Int32
	fa := newMicroservicesFieldAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/agent/microservices") {
			gets.Add(1)
			_, _ = w.Write(fixtureControllerMicroservicesKnowledgeJSON())
			return
		}
		http.NotFound(w, r)
	})
	fa.state.SetInitialization(false)

	_ = fa.processChanges(map[string]any{"microserviceKnowledge": true})
	if gets.Load() != 1 {
		t.Fatalf("expected one GET microservices, got %d", gets.Load())
	}

	items, err := store.GetInstance().LoadControllerMicroservices()
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	if len(items) != 1 || items[0].MicroserviceUUID != "ms-knowledge-catalog" {
		t.Fatalf("unexpected stored microservices: %+v", items)
	}
	if !items[0].Knowledge.HasItems() || items[0].Knowledge.BindPath != "/knowledge" || items[0].Knowledge.Items[0].Name != "product-docs" {
		t.Fatalf("expected knowledge catalog persisted, got %+v", items[0].Knowledge)
	}
}

func TestProcessChanges_MicroserviceListAndKnowledgeShareOneGET(t *testing.T) {
	var gets atomic.Int32
	fa := newMicroservicesFieldAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/agent/microservices") {
			gets.Add(1)
			_, _ = w.Write(fixtureControllerMicroservicesKnowledgeJSON())
			return
		}
		http.NotFound(w, r)
	})
	fa.state.SetInitialization(false)

	_ = fa.processChanges(map[string]any{
		"microserviceList":      true,
		"microserviceKnowledge": true,
		"microserviceModels":    true,
		"unknownExtraFlag":      true,
	})
	if gets.Load() != 1 {
		t.Fatalf("list and catalog flags must share one GET, got %d", gets.Load())
	}
}

func TestProcessChanges_MicroserviceKnowledgeFalseDoesNotFetch(t *testing.T) {
	var gets atomic.Int32
	fa := newMicroservicesFieldAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/agent/microservices") {
			gets.Add(1)
			_, _ = w.Write(fixtureControllerMicroservicesKnowledgeJSON())
			return
		}
		http.NotFound(w, r)
	})
	fa.state.SetInitialization(false)

	_ = fa.processChanges(map[string]any{"microserviceKnowledge": false})
	if gets.Load() != 0 {
		t.Fatalf("false catalog flag must not GET microservices, got %d", gets.Load())
	}
}

func TestParseMicroservice_KnowledgeCatalogAndUnknownKeysIgnored(t *testing.T) {
	ms, err := parseMicroservice(map[string]any{
		"uuid":                  "ms-knowledge",
		"imageId":               "alpine:3.19",
		"futureControllerField": "ignored",
		"knowledge": map[string]any{
			"bindPath":    "/knowledge",
			"permissions": "ro",
			"items": []any{
				map[string]any{"name": "product-docs", "uuid": "ignored"},
			},
		},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ms.Knowledge == nil || ms.Knowledge.BindPath != "/knowledge" || len(ms.Knowledge.Items) != 1 || ms.Knowledge.Items[0].Name != "product-docs" {
		t.Fatalf("knowledge catalog mismatch: %+v", ms.Knowledge)
	}
	if ms.Knowledge.Items[0].Name != "product-docs" {
		t.Fatalf("bind items must use name only, got %+v", ms.Knowledge.Items)
	}
}
