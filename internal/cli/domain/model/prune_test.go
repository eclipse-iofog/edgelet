package model

import (
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/cli/run"
)

type fakeClient struct {
	method string
	path   string
	body   any
	data   map[string]any
	err    error
}

func (f *fakeClient) Request(method, path string, body any) (map[string]any, error) {
	f.method = method
	f.path = path
	if method == "POST" {
		f.body = body
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.data != nil {
		return f.data, nil
	}
	return map[string]any{"mode": "dangling", "removed": []any{"leftover"}}, nil
}

func (f *fakeClient) RequestMultipartFile(string, string, string, string, map[string]string) (map[string]any, error) {
	return nil, run.NewCLIError(run.CodeInternal, "unused", nil)
}

func (f *fakeClient) IsDaemonRunning() bool { return true }

func TestPrune_PostsDanglingMode(t *testing.T) {
	client := &fakeClient{}
	result, err := Prune(client, []string{"dangling"})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if client.method != "POST" || client.path != "/v1/models:prune?mode=dangling" {
		t.Fatalf("unexpected request %s %s", client.method, client.path)
	}
	if result == nil || result.Data["mode"] != "dangling" {
		t.Fatalf("unexpected result %+v", result)
	}
}

func TestPrune_RejectsInvalidMode(t *testing.T) {
	_, err := Prune(&fakeClient{}, []string{"volumes"})
	if err == nil || !strings.Contains(err.Error(), "model prune supports only dangling mode") {
		t.Fatalf("expected dangling-only error, got %v", err)
	}
}
