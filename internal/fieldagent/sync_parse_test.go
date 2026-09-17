package fieldagent

import (
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/store"
)

func TestParseRegistry_TypeCAInsecureRoundTrip(t *testing.T) {
	openFieldAgentTestDB(t)

	parsed := parseRegistry(map[string]any{
		"id":       float64(7),
		"url":      "https://registry.example.com",
		"isPublic": true,
		"type":     "hf",
		"ca":       "Y2E=",
		"insecure": true,
	})
	if parsed.Type != models.RegistryTypeHF {
		t.Fatalf("type=%q want hf", parsed.Type)
	}
	if parsed.CAB64 != "Y2E=" {
		t.Fatalf("ca=%q", parsed.CAB64)
	}
	if !parsed.Insecure {
		t.Fatal("expected insecure true")
	}

	if err := store.GetInstance().SaveControllerRegistries([]*models.Registry{parsed}); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := store.GetInstance().LoadControllerRegistries()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var got *models.Registry
	for _, reg := range loaded {
		if reg.ID == 7 {
			got = reg
			break
		}
	}
	if got == nil {
		t.Fatalf("registry 7 missing: %+v", loaded)
	}
	if got.Type != models.RegistryTypeHF || got.CAB64 != "Y2E=" || !got.Insecure {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestParseRegistry_OmitsTypeDefaultsToOCI(t *testing.T) {
	parsed := parseRegistry(map[string]any{
		"id":       float64(8),
		"url":      "docker.io",
		"isPublic": true,
	})
	if parsed.Type != models.RegistryTypeOCI {
		t.Fatalf("expected oci default, got %q", parsed.Type)
	}
	if parsed.Insecure {
		t.Fatal("expected insecure false")
	}
}

func TestParseMicroservice_CmdOnlyMapsToCommands(t *testing.T) {
	ms, err := parseMicroservice(map[string]any{
		"uuid":    "ms-cmd",
		"imageId": "alpine:3.19",
		"cmd":     []any{"--serve", "--port=8080"},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if models.UsesImageDefault(ms.Commands) || (*ms.Commands)[0] != "--serve" {
		t.Fatalf("commands=%v", ms.Commands)
	}
	if len(ms.Args) != 2 || ms.Args[0] != "--serve" {
		t.Fatalf("args=%v", ms.Args)
	}
}

func TestParseMicroservice_CommandsOnlyMapsToCommands(t *testing.T) {
	ms, err := parseMicroservice(map[string]any{
		"uuid":     "ms-commands",
		"imageId":  "alpine:3.19",
		"commands": []any{"--serve"},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if models.UsesImageDefault(ms.Commands) || (*ms.Commands)[0] != "--serve" {
		t.Fatalf("commands=%v", ms.Commands)
	}
}

func TestParseMicroservice_CommandsPreferredOverCmd(t *testing.T) {
	ms, err := parseMicroservice(map[string]any{
		"uuid":     "ms-both",
		"imageId":  "alpine:3.19",
		"cmd":      []any{"--from-cmd"},
		"commands": []any{"--from-commands"},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if models.UsesImageDefault(ms.Commands) || (*ms.Commands)[0] != "--from-commands" {
		t.Fatalf("expected commands to win, got %v", ms.Commands)
	}
}

func TestParseMicroservice_EmptyCmdAndCommandsUseImageDefault(t *testing.T) {
	ms, err := parseMicroservice(map[string]any{
		"uuid":       "ms-empty",
		"imageId":    "alpine:3.19",
		"cmd":        []any{},
		"commands":   []any{},
		"entrypoint": []any{},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !models.UsesImageDefault(ms.Commands) || ms.Commands != nil {
		t.Fatalf("expected nil commands for image default, got %#v", ms.Commands)
	}
	if !models.UsesImageDefault(ms.Entrypoint) || ms.Entrypoint != nil {
		t.Fatalf("expected nil entrypoint for image default, got %#v", ms.Entrypoint)
	}
}

func TestParseMicroservice_CatalogAndUnknownKeysIgnored(t *testing.T) {
	ms, err := parseMicroservice(map[string]any{
		"uuid":                   "ms-catalog",
		"imageId":                "alpine:3.19",
		"futureControllerField":  "ignored",
		"runAsGroup":             "0",
		"cpus":                   2.5,
		"memoryReservation":      float64(128),
		"readOnlyRootFilesystem": true,
		"workingDir":             "/app",
		"sysctls":                map[string]any{"net.ipv4.tcp_syncookies": "1"},
		"models": map[string]any{
			"bindPath":    "/models",
			"permissions": "ro",
			"items": []any{
				map[string]any{"name": "test-model", "uuid": "ignored"},
			},
		},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ms.Models == nil || ms.Models.BindPath != "/models" || len(ms.Models.Items) != 1 || ms.Models.Items[0].Name != "test-model" {
		t.Fatalf("catalog mismatch: %+v", ms.Models)
	}
	if ms.RunAsGroup == nil || *ms.RunAsGroup != "0" {
		t.Fatalf("runAsGroup=%v", ms.RunAsGroup)
	}
	if ms.Cpus == nil || *ms.Cpus != 2.5 {
		t.Fatalf("cpus=%v", ms.Cpus)
	}
	if !ms.ReadOnlyRootFilesystem {
		t.Fatal("expected readOnlyRootFilesystem")
	}
}
