package cmd

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestKnowledgePullHumanSuccessOnStderr(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, stderr, code := runCLI(t, client, "knowledge", "pull", "product-docs")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("expected empty stdout for human knowledge pull, got %q", stdout)
	}
	if !strings.Contains(stderr, "knowledge pulled successfully") {
		t.Fatalf("expected success on stderr, got %q", stderr)
	}
}

func TestKnowledgePullJSONStdoutOnly(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, stderr, code := runCLI(t, client, "-o", "json", "knowledge", "pull", "product-docs")
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

func TestKnowledgePullWithSpecFlags(t *testing.T) {
	client := &fakeClient{running: true}
	_, stderr, code := runCLI(t, client, "knowledge", "pull", "wiki-faiss",
		"--repo", "acme/wiki",
		"--registry", "3",
		"--files", "data/**/*.jsonl",
	)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "knowledge pulled successfully") {
		t.Fatalf("expected success on stderr, got %q", stderr)
	}
}

func TestKnowledgePullSpecFlagsRequireRegistry(t *testing.T) {
	client := &fakeClient{running: true}
	_, stderr, code := runCLI(t, client, "knowledge", "pull", "wiki-faiss", "--files", "data/**/*.jsonl")
	if code == 0 {
		t.Fatalf("expected non-zero exit, stderr=%q", stderr)
	}
	if !strings.Contains(stderr, "repo and --registry") {
		t.Fatalf("expected spec validation error, got %q", stderr)
	}
}

func TestKnowledgeLsJSON(t *testing.T) {
	client := &fakeClient{
		running: true,
		gets: map[string]map[string]any{
			"GET /v1/knowledge": {
				"count": 1,
				"items": []any{
					map[string]any{"name": "product-docs", "state": "Ready"},
				},
			},
		},
	}
	stdout, _, code := runCLI(t, client, "-o", "json", "knowledge", "ls")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "product-docs") {
		t.Fatalf("expected knowledge list json, got %q", stdout)
	}
}
