package cmd

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestModelPullHumanSuccessOnStderr(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, stderr, code := runCLI(t, client, "model", "pull", "llama-2-7b-q2k")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("expected empty stdout for human model pull, got %q", stdout)
	}
	if !strings.Contains(stderr, "model pulled successfully") {
		t.Fatalf("expected success on stderr, got %q", stderr)
	}
}

func TestModelPullJSONStdoutOnly(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, stderr, code := runCLI(t, client, "-o", "json", "model", "pull", "llama-2-7b-q2k")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("expected no stderr for json, got %q", stderr)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &decoded); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if decoded["status"] != "succeeded" {
		t.Fatalf("unexpected payload: %#v", decoded)
	}
}

func TestModelPullWithSpecFlags(t *testing.T) {
	client := &fakeClient{running: true}
	_, stderr, code := runCLI(t, client, "model", "pull", "tiny-gpt2",
		"--repo", "hf-internal-testing/tiny-random-gpt2",
		"--registry", "3",
		"--files", "config.json",
	)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "model pulled successfully") {
		t.Fatalf("expected success on stderr, got %q", stderr)
	}
}

func TestModelPullSpecFlagsRequireRegistry(t *testing.T) {
	client := &fakeClient{running: true}
	_, stderr, code := runCLI(t, client, "model", "pull", "tiny-gpt2", "--files", "config.json")
	if code == 0 {
		t.Fatalf("expected non-zero exit, stderr=%q", stderr)
	}
	if !strings.Contains(stderr, "repo and --registry") {
		t.Fatalf("expected spec validation error, got %q", stderr)
	}
}

func TestModelLsJSON(t *testing.T) {
	client := &fakeClient{
		running: true,
		gets: map[string]map[string]any{
			"GET /v1/models": {
				"count": 1,
				"items": []any{
					map[string]any{"name": "llama-2-7b-q2k", "state": "Ready"},
				},
			},
		},
	}
	stdout, _, code := runCLI(t, client, "-o", "json", "model", "ls")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "llama-2-7b-q2k") {
		t.Fatalf("expected model list json, got %q", stdout)
	}
}
