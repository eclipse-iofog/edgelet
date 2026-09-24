package cmd

import (
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/cli/domain/config"
	"github.com/eclipse-iofog/edgelet/internal/cli/run"
)

func TestConfigLongFlagPatch(t *testing.T) {
	client := &fakeClient{
		running: true,
		gets: map[string]map[string]any{
			"GET /v1/system/config": {
				"changeFrequencySeconds": 20,
			},
			"PATCH /v1/system/config": {
				"status": "ok",
			},
		},
	}
	stdout, stderr, code := runCLI(t, client, "config", "--change-frequency-seconds", "10")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("expected empty stdout in human mode, got %q", stdout)
	}
	if !strings.Contains(stderr, "config update:") {
		t.Fatalf("expected config update on stderr, got stderr=%q", stderr)
	}
	if !strings.Contains(stderr, "✔") {
		t.Fatalf("expected success marker on stderr, got stderr=%q", stderr)
	}
}

func TestConfigShortAliasFlags(t *testing.T) {
	client := &fakeClient{
		running: true,
		gets: map[string]map[string]any{
			"GET /v1/system/config": {
				"controllerUrl":          "http://old/api/v3",
				"changeFrequencySeconds": 20,
				"statusFrequencySeconds": 30,
			},
			"PATCH /v1/system/config": {
				"status": "ok",
			},
		},
	}
	stdout, stderr, code := runCLI(t, client,
		"config",
		"--a", "http://localhost:51121/api/v3",
		"--cf", "10",
		"--sf", "10",
	)
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("expected empty stdout in human mode, got %q", stdout)
	}
	if !strings.Contains(stderr, "changeFrequencySeconds") {
		t.Fatalf("expected accepted keys on stderr, got stderr=%q", stderr)
	}
}

func TestConfigPartialRejectionExit2(t *testing.T) {
	client := &fakeClient{
		running: true,
		gets: map[string]map[string]any{
			"GET /v1/system/config": {
				"changeFrequencySeconds": 20,
			},
			"PATCH /v1/system/config": {
				"status": "ok",
				"errorMap": map[string]any{
					"changeFrequencySeconds": "value out of range",
				},
			},
		},
	}
	stdout, stderr, code := runCLI(t, client, "config", "--cf", "10")
	if code != run.ExitInvalidArgument {
		t.Fatalf("expected exit 2, got %d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("expected empty stdout, got %q", stdout)
	}
	if !strings.Contains(stderr, "✘") || !strings.Contains(stderr, "rejected:") {
		t.Fatalf("expected rejection UX on stderr, got stderr=%q", stderr)
	}
}

func TestConfigJSONStdoutOnly(t *testing.T) {
	client := &fakeClient{
		running: true,
		gets: map[string]map[string]any{
			"GET /v1/system/config": {
				"changeFrequencySeconds": 20,
			},
			"PATCH /v1/system/config": {
				"status": "ok",
			},
		},
	}
	stdout, stderr, code := runCLI(t, client, "-o", "json", "config", "--cf", "10")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("expected no stderr UX for json output, got %q", stderr)
	}
	if !strings.Contains(stdout, `"status"`) {
		t.Fatalf("expected JSON on stdout, got %q", stdout)
	}
}

func TestConfigRequiresAtLeastOneFlag(t *testing.T) {
	client := &fakeClient{running: true}
	_, stderr, code := runCLI(t, client, "config")
	if code != run.ExitInvalidArgument {
		t.Fatalf("expected exit 2, got %d stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "at least one config flag is required") {
		t.Fatalf("expected required flag error, got stderr=%q", stderr)
	}
}

func TestConfigRejectsUnknownSubcommand(t *testing.T) {
	client := &fakeClient{running: true}
	_, _, code := runCLI(t, client, "config", "cf", "10")
	if code == 0 {
		t.Fatal("expected non-zero exit for unknown config subcommand")
	}
}

func TestConfigRejectsLegacySetSubcommand(t *testing.T) {
	client := &fakeClient{running: true}
	_, _, code := runCLI(t, client, "config", "set", "networkInterface", "eth0")
	if code == 0 {
		t.Fatal("expected non-zero exit for config set")
	}
}

func TestConfigHelpShowsFlagsNotSettingsTable(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "config", "--help")
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if !strings.Contains(stdout, "--controller-url") || !strings.Contains(stdout, "--change-frequency-seconds") {
		t.Fatalf("expected long flags in help, got stdout=%q", stdout)
	}
	if !strings.Contains(stdout, "--disk-limit-gib") {
		t.Fatalf("expected explicit gib flag name in help, got stdout=%q", stdout)
	}
	if !strings.Contains(stdout, "Alias: --a") || !strings.Contains(stdout, "Alias: --cf") {
		t.Fatalf("expected alias documentation in flag help, got stdout=%q", stdout)
	}
	if strings.Contains(stdout, "Settings:") {
		t.Fatalf("expected Settings table removed from long help, got stdout=%q", stdout)
	}
	if strings.Contains(stdout, "--device-scan-frequency") || strings.Contains(stdout, "--sd") {
		t.Fatalf("expected device scan flags removed from help, got stdout=%q", stdout)
	}
}

func TestConfigLongHelpIsShortIntro(t *testing.T) {
	long := config.CommandLong()
	if strings.Contains(long, "Settings:") {
		t.Fatalf("CommandLong should not list settings: %q", long)
	}
	if !strings.Contains(long, "config cert") {
		t.Fatalf("expected cert mention in intro: %q", long)
	}
}
