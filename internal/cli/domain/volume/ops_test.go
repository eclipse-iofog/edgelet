package volume

import (
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/cli/run"
)

type fakeClient struct {
	method string
	path   string
	data   map[string]any
	err    error
}

func (f *fakeClient) Request(method, path string, _ any) (map[string]any, error) {
	f.method = method
	f.path = path
	if f.err != nil {
		return nil, f.err
	}
	if f.data != nil {
		return f.data, nil
	}
	return map[string]any{"status": "ok"}, nil
}

func (f *fakeClient) RequestMultipartFile(string, string, string, string, map[string]string) (map[string]any, error) {
	return nil, run.NewCLIError(run.CodeInternal, "unused", nil)
}

func (f *fakeClient) IsDaemonRunning() bool { return true }

func TestRemoveShared_DeletesByName(t *testing.T) {
	client := &fakeClient{data: map[string]any{"name": "shared-config", "status": "ok"}}
	result, err := RemoveShared(client, "shared-config", false)
	if err != nil {
		t.Fatalf("remove shared: %v", err)
	}
	if client.method != "DELETE" || client.path != "/v1/volumes/shared/shared-config" {
		t.Fatalf("unexpected request %s %s", client.method, client.path)
	}
	if result.Data["name"] != "shared-config" {
		t.Fatalf("unexpected result %+v", result)
	}
}

func TestRemovePrivate_DeletesByUUID(t *testing.T) {
	client := &fakeClient{}
	_, err := RemovePrivate(client, "ms-uuid", "data", true)
	if err != nil {
		t.Fatalf("remove private: %v", err)
	}
	if client.method != "DELETE" {
		t.Fatalf("expected DELETE, got %s", client.method)
	}
	if !strings.HasPrefix(client.path, "/v1/volumes/ms-uuid?") || !strings.Contains(client.path, "name=data") || !strings.Contains(client.path, "force=true") {
		t.Fatalf("unexpected path %s", client.path)
	}
}

func TestPrune_DefaultDryRun(t *testing.T) {
	client := &fakeClient{data: map[string]any{"dryRun": true}}
	result, err := Prune(client, PruneOptions{Orphans: true})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if client.method != "POST" || !strings.Contains(client.path, "/v1/volumes:prune?") {
		t.Fatalf("unexpected request %s %s", client.method, client.path)
	}
	if !strings.Contains(client.path, "dryRun=true") {
		t.Fatalf("expected dry-run, got %s", client.path)
	}
	dryRun, ok := result.Data["dryRun"].(bool)
	if !ok || !dryRun {
		t.Fatalf("unexpected result %+v", result)
	}
}

func TestRemoveShared_RequiresName(t *testing.T) {
	_, err := RemoveShared(&fakeClient{}, "  ", false)
	if err == nil || !strings.Contains(err.Error(), "shared volume name is required") {
		t.Fatalf("expected name required, got %v", err)
	}
}
