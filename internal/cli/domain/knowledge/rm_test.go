package knowledge

import (
	"strings"
	"testing"
)

func TestRemove_DeletesByName(t *testing.T) {
	client := &fakeClient{data: map[string]any{"name": "product-docs", "status": "ok"}}
	result, err := Remove(client, "product-docs")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if client.method != "DELETE" || client.path != "/v1/knowledge/product-docs" {
		t.Fatalf("unexpected request %s %s", client.method, client.path)
	}
	if result.Data["name"] != "product-docs" {
		t.Fatalf("unexpected result %+v", result)
	}
}

func TestRemove_RequiresName(t *testing.T) {
	_, err := Remove(&fakeClient{}, "  ")
	if err == nil || !strings.Contains(err.Error(), "knowledge name is required") {
		t.Fatalf("expected name required, got %v", err)
	}
}
