package knowledge

import (
	"strings"
	"testing"
)

func TestPrune_PostsDanglingMode(t *testing.T) {
	client := &fakeClient{}
	result, err := Prune(client, []string{"dangling"})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if client.method != "POST" || client.path != "/v1/knowledge:prune?mode=dangling" {
		t.Fatalf("unexpected request %s %s", client.method, client.path)
	}
	if result == nil || result.Data["mode"] != "dangling" {
		t.Fatalf("unexpected result %+v", result)
	}
}

func TestPrune_RejectsInvalidMode(t *testing.T) {
	_, err := Prune(&fakeClient{}, []string{"volumes"})
	if err == nil || !strings.Contains(err.Error(), "knowledge prune supports only dangling mode") {
		t.Fatalf("expected dangling-only error, got %v", err)
	}
}
