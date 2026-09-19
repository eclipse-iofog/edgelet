package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/cli/run"
)

func TestVolumeLsJSON(t *testing.T) {
	client := &fakeClient{
		running: true,
		gets: map[string]map[string]any{
			"GET /v1/volumes": {
				"count": 1,
				"volumes": []any{
					map[string]any{"name": "shared-config", "scope": "shared", "uuid": nil},
				},
			},
		},
	}
	stdout, _, code := runCLI(t, client, "-o", "json", "volume", "ls")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "shared-config") {
		t.Fatalf("expected volume list json, got %q", stdout)
	}
	if client.lastMethod != "GET" || client.lastPath != "/v1/volumes" {
		t.Fatalf("unexpected request %s %s", client.lastMethod, client.lastPath)
	}
}

func TestVolumeRmSharedUsesSharedDeletePath(t *testing.T) {
	client := &fakeClient{
		running: true,
		gets: map[string]map[string]any{
			"DELETE /v1/volumes/shared/shared-config": {
				"status": "ok",
				"name":   "shared-config",
				"scope":  "shared",
			},
		},
	}
	_, stderr, code := runCLI(t, client, "volume", "rm", "--shared", "shared-config")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if client.lastMethod != "DELETE" || client.lastPath != "/v1/volumes/shared/shared-config" {
		t.Fatalf("expected shared delete path, got %s %s", client.lastMethod, client.lastPath)
	}
}

func TestVolumeRmSharedForceQuery(t *testing.T) {
	client := &fakeClient{running: true}
	_, _, code := runCLI(t, client, "volume", "rm", "--shared", "shared-config", "--force")
	if code != 0 {
		t.Fatalf("exit=%d path=%s", code, client.lastPath)
	}
	if client.lastPath != "/v1/volumes/shared/shared-config?force=true" {
		t.Fatalf("expected force query, got %s", client.lastPath)
	}
}

func TestVolumeRmPrivateUsesUUIDPath(t *testing.T) {
	client := &fakeClient{running: true}
	_, _, code := runCLI(t, client, "volume", "rm", "ms-uuid", "data", "--force")
	if code != 0 {
		t.Fatalf("exit=%d path=%s", code, client.lastPath)
	}
	if client.lastMethod != "DELETE" {
		t.Fatalf("expected DELETE, got %s", client.lastMethod)
	}
	if !strings.HasPrefix(client.lastPath, "/v1/volumes/ms-uuid?") || !strings.Contains(client.lastPath, "force=true") || !strings.Contains(client.lastPath, "name=data") {
		t.Fatalf("unexpected private delete path %s", client.lastPath)
	}
}

func TestVolumeRmSharedRejectsUUIDArg(t *testing.T) {
	client := &fakeClient{running: true}
	_, stderr, code := runCLI(t, client, "volume", "rm", "ms-uuid", "--shared", "shared-config")
	if code != run.ExitInvalidArgument {
		t.Fatalf("expected invalid argument, got %d stderr=%q", code, stderr)
	}
	if client.lastPath != "" {
		t.Fatalf("expected no request, got %s", client.lastPath)
	}
}

func TestVolumePruneDefaultIsDryRun(t *testing.T) {
	client := &fakeClient{
		running: true,
		gets: map[string]map[string]any{
			"POST /v1/volumes:prune?dryRun=true&orphans=true": {
				"dryRun":     true,
				"candidates": []any{},
			},
		},
	}
	stdout, _, code := runCLI(t, client, "-o", "json", "volume", "prune")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q path=%s", code, stdout, client.lastPath)
	}
	if client.lastMethod != "POST" || !strings.Contains(client.lastPath, "/v1/volumes:prune?") {
		t.Fatalf("unexpected prune path %s %s", client.lastMethod, client.lastPath)
	}
	if !strings.Contains(client.lastPath, "dryRun=true") || strings.Contains(client.lastPath, "yes=true") {
		t.Fatalf("expected dry-run prune, got %s", client.lastPath)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &decoded); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
}

func TestVolumePruneYesDestroys(t *testing.T) {
	client := &fakeClient{running: true}
	_, _, code := runCLI(t, client, "volume", "prune", "--orphans", "--yes")
	if code != 0 {
		t.Fatalf("exit=%d path=%s", code, client.lastPath)
	}
	if !strings.Contains(client.lastPath, "yes=true") || !strings.Contains(client.lastPath, "dryRun=false") {
		t.Fatalf("expected destroy prune, got %s", client.lastPath)
	}
}

func TestSystemPruneVolumesFailsClosed(t *testing.T) {
	client := &fakeClient{running: true}
	_, stderr, code := runCLI(t, client, "system", "prune", "volumes")
	if code != run.ExitInvalidArgument {
		t.Fatalf("expected invalid argument, got %d stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "edgelet volume prune") {
		t.Fatalf("expected pointer at volume prune, got %q", stderr)
	}
	if client.lastPath != "" {
		t.Fatalf("expected no prune request, got %s", client.lastPath)
	}
}

func TestDeprovisionPurgeVolumesQuery(t *testing.T) {
	client := &fakeClient{
		running: true,
		gets: map[string]map[string]any{
			"DELETE /v1/system/provision?purgeVolumes=true": {"status": "ok"},
		},
	}
	_, stderr, code := runCLI(t, client, "deprovision", "--scope", "all", "--purge-volumes")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q path=%s", code, stderr, client.lastPath)
	}
	if client.lastMethod != "DELETE" || !strings.Contains(client.lastPath, "purgeVolumes=true") {
		t.Fatalf("expected purgeVolumes query, got %s %s", client.lastMethod, client.lastPath)
	}
}

func TestMSRmCleanupQuery(t *testing.T) {
	client := &fakeClient{running: true}
	_, _, code := runCLI(t, client, "ms", "rm", "ms-1", "--cleanup")
	if code != 0 {
		t.Fatalf("exit=%d path=%s", code, client.lastPath)
	}
	if client.lastMethod != "DELETE" || client.lastPath != "/v1/ms/ms-1?cleanup=true" {
		t.Fatalf("expected cleanup query, got %s %s", client.lastMethod, client.lastPath)
	}
}

func TestMSRmWithoutCleanupOmitsQuery(t *testing.T) {
	client := &fakeClient{running: true}
	_, _, code := runCLI(t, client, "ms", "rm", "ms-1")
	if code != 0 {
		t.Fatalf("exit=%d path=%s", code, client.lastPath)
	}
	if client.lastPath != "/v1/ms/ms-1" {
		t.Fatalf("expected retain path, got %s", client.lastPath)
	}
}
