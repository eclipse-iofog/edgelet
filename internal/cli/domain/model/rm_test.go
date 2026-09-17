package model

import (
	"strings"
	"testing"
)

func TestRemove_DeletesByName(t *testing.T) {
	client := &fakeClient{data: map[string]any{"name": "llama-2-7b-q2k", "status": "ok"}}
	result, err := Remove(client, "llama-2-7b-q2k")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if client.method != "DELETE" || client.path != "/v1/models/llama-2-7b-q2k" {
		t.Fatalf("unexpected request %s %s", client.method, client.path)
	}
	if result.Data["name"] != "llama-2-7b-q2k" {
		t.Fatalf("unexpected result %+v", result)
	}
}

func TestRemove_RequiresName(t *testing.T) {
	_, err := Remove(&fakeClient{}, "  ")
	if err == nil || !strings.Contains(err.Error(), "model name is required") {
		t.Fatalf("expected name required, got %v", err)
	}
}
