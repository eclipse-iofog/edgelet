package model

import (
	"context"
	"strings"
	"testing"
)

func TestPull_PostsNameAndPolls(t *testing.T) {
	client := &fakeClient{data: map[string]any{
		"status":      "succeeded",
		"name":        "llama-2-7b-q2k",
		"operationId": "op-1",
	}}
	// first Request is POST start; subsequent GET status uses same data
	result, err := Pull(context.Background(), client, nil, PullRequest{Name: "llama-2-7b-q2k"})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if !strings.Contains(result.Human, "llama-2-7b-q2k") {
		t.Fatalf("unexpected human: %s", result.Human)
	}
}

func TestPull_RequiresName(t *testing.T) {
	_, err := Pull(context.Background(), &fakeClient{}, nil, PullRequest{Name: "  "})
	if err == nil || !strings.Contains(err.Error(), "model name is required") {
		t.Fatalf("expected name required, got %v", err)
	}
}

func TestPull_PostsSpecFields(t *testing.T) {
	client := &fakeClient{data: map[string]any{
		"status":      "succeeded",
		"name":        "tiny-gpt2",
		"operationId": "op-1",
	}}
	_, err := Pull(context.Background(), client, nil, PullRequest{
		Name:       "tiny-gpt2",
		Repo:       "hf-internal-testing/tiny-random-gpt2",
		Revision:   "71034c5d8bde858ff824298bdedc65515b97d2b9",
		RegistryID: 3,
		Files:      []string{"config.json"},
		Format:     "unknown",
	})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	body, ok := client.body.(map[string]any)
	if !ok {
		t.Fatalf("expected POST body, got %#v", client.body)
	}
	if body["name"] != "tiny-gpt2" || body["repo"] != "hf-internal-testing/tiny-random-gpt2" || body["registryId"] != 3 {
		t.Fatalf("unexpected POST body: %#v", body)
	}
}

func TestPull_SpecRequiresRepoAndRegistry(t *testing.T) {
	_, err := Pull(context.Background(), &fakeClient{}, nil, PullRequest{
		Name:  "tiny-gpt2",
		Files: []string{"config.json"},
	})
	if err == nil || !strings.Contains(err.Error(), "repo and --registry") {
		t.Fatalf("expected spec validation error, got %v", err)
	}
}
