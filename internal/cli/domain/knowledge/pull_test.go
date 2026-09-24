package knowledge

import (
	"context"
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

func TestPull_PostsNameAndPolls(t *testing.T) {
	client := &fakeClient{data: map[string]any{
		"status":      "succeeded",
		"name":        "product-docs",
		"operationId": "op-1",
	}}
	result, err := Pull(context.Background(), client, nil, PullRequest{Name: "product-docs"})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if !strings.Contains(result.Human, "product-docs") {
		t.Fatalf("unexpected human: %s", result.Human)
	}
}

func TestPull_RequiresName(t *testing.T) {
	_, err := Pull(context.Background(), &fakeClient{}, nil, PullRequest{Name: "  "})
	if err == nil || !strings.Contains(err.Error(), "knowledge name is required") {
		t.Fatalf("expected name required, got %v", err)
	}
}

func TestPull_PostsSpecFields(t *testing.T) {
	client := &fakeClient{data: map[string]any{
		"status":      "succeeded",
		"name":        "wiki-faiss",
		"operationId": "op-1",
	}}
	_, err := Pull(context.Background(), client, nil, PullRequest{
		Name:       "wiki-faiss",
		Repo:       "acme/wiki",
		Revision:   "9f3c111122223333444455556666777788889999",
		RegistryID: 3,
		Files:      []string{"data/**/*.jsonl"},
		Format:     "jsonl",
	})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	body, ok := client.body.(map[string]any)
	if !ok {
		t.Fatalf("expected POST body, got %#v", client.body)
	}
	if body["name"] != "wiki-faiss" || body["repo"] != "acme/wiki" || body["registryId"] != 3 {
		t.Fatalf("unexpected POST body: %#v", body)
	}
}

func TestPull_SpecRequiresRepoAndRegistry(t *testing.T) {
	_, err := Pull(context.Background(), &fakeClient{}, nil, PullRequest{
		Name:  "wiki-faiss",
		Files: []string{"data/**/*.jsonl"},
	})
	if err == nil || !strings.Contains(err.Error(), "repo and --registry") {
		t.Fatalf("expected spec validation error, got %v", err)
	}
}
