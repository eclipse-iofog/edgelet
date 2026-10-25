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

func TestParseMicroservice_SupplementalGroupsSorted(t *testing.T) {
	ms, err := parseMicroservice(map[string]any{
		"uuid":               "ms-groups",
		"imageId":            "alpine:3.19",
		"supplementalGroups": []any{float64(44), float64(993)},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ms.SupplementalGroups) != 2 || ms.SupplementalGroups[0] != 44 || ms.SupplementalGroups[1] != 993 {
		t.Fatalf("groups=%v", ms.SupplementalGroups)
	}

	reversed, err := parseMicroservice(map[string]any{
		"uuid":               "ms-groups-rev",
		"imageId":            "alpine:3.19",
		"supplementalGroups": []any{float64(993), float64(44)},
	})
	if err != nil {
		t.Fatalf("parse reversed: %v", err)
	}
	if len(reversed.SupplementalGroups) != 2 || reversed.SupplementalGroups[0] != 44 || reversed.SupplementalGroups[1] != 993 {
		t.Fatalf("reversed groups=%v", reversed.SupplementalGroups)
	}

	omitted, err := parseMicroservice(map[string]any{
		"uuid":    "ms-omit",
		"imageId": "alpine:3.19",
	})
	if err != nil {
		t.Fatalf("parse omitted: %v", err)
	}
	if len(omitted.SupplementalGroups) != 0 {
		t.Fatalf("omitted groups=%v", omitted.SupplementalGroups)
	}
}

func TestParseMicroservice_SupplementalGroupsNumbers(t *testing.T) {
	whole, err := parseMicroservice(map[string]any{
		"uuid":               "ms-whole",
		"imageId":            "alpine:3.19",
		"supplementalGroups": []any{float64(44)},
	})
	if err != nil {
		t.Fatalf("parse 44.0: %v", err)
	}
	if len(whole.SupplementalGroups) != 1 || whole.SupplementalGroups[0] != 44 {
		t.Fatalf("groups=%v", whole.SupplementalGroups)
	}

	if _, err := parseMicroservice(map[string]any{
		"uuid":               "ms-fraction",
		"imageId":            "alpine:3.19",
		"supplementalGroups": []any{44.5},
	}); err == nil {
		t.Fatal("expected 44.5 to be rejected")
	}
	if _, err := parseMicroservice(map[string]any{
		"uuid":               "ms-string-number",
		"imageId":            "alpine:3.19",
		"supplementalGroups": []any{"44"},
	}); err == nil {
		t.Fatal(`expected "44" to be rejected`)
	}
	if _, err := parseMicroservice(map[string]any{
		"uuid":               "ms-name",
		"imageId":            "alpine:3.19",
		"supplementalGroups": []any{"render"},
	}); err == nil {
		t.Fatal(`expected "render" to be rejected`)
	}
}

func TestParseMicroservice_VolumeScope(t *testing.T) {
	ms, err := parseMicroservice(map[string]any{
		"uuid":    "ms-vol",
		"imageId": "alpine:3.19",
		"volumeMappings": []any{
			map[string]any{
				"hostDestination":      "config",
				"containerDestination": "/app/config",
				"accessMode":           "rw",
				"type":                 "volume",
				"scope":                "shared",
			},
			map[string]any{
				"hostDestination":      "data",
				"containerDestination": "/data",
				"accessMode":           "rw",
				"type":                 "volume",
				"scope":                "SHARED",
			},
			map[string]any{
				"hostDestination":      "/var/lib/data",
				"containerDestination": "/host",
				"accessMode":           "rw",
				"type":                 "bind",
				"scope":                "shared",
			},
			map[string]any{
				"hostDestination":      "foo-vol",
				"containerDestination": "/foo",
				"accessMode":           "rw",
				"type":                 "volume",
				"scope":                "foo",
			},
		},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ms.VolumeMappings) != 4 {
		t.Fatalf("len=%d", len(ms.VolumeMappings))
	}
	if ms.VolumeMappings[0].Scope != models.VolumeScopeShared {
		t.Fatalf("shared scope=%q", ms.VolumeMappings[0].Scope)
	}
	if ms.VolumeMappings[1].Scope != models.VolumeScopePrivate {
		t.Fatalf("SHARED coerced=%q", ms.VolumeMappings[1].Scope)
	}
	if ms.VolumeMappings[2].Type != models.VolumeMappingTypeBind || ms.VolumeMappings[2].Scope != models.VolumeScopePrivate {
		t.Fatalf("BIND mapping=%+v", ms.VolumeMappings[2])
	}
	if ms.VolumeMappings[3].Scope != models.VolumeScopePrivate {
		t.Fatalf("unknown scope=%q", ms.VolumeMappings[3].Scope)
	}
}
