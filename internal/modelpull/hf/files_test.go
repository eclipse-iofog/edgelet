package hf

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestExpandFiles_ExactAndGlob(t *testing.T) {
	repo := []string{
		"config.json",
		"llama-2-7b-chat.Q4_K_M.gguf",
		"llama-2-7b-chat.Q5_K_M.gguf",
		"llama-2-7b-chat.Q8_0.gguf",
		"onnx/model.onnx",
		"README.md",
	}

	got, err := ExpandFiles([]string{"*.gguf"}, repo)
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(got) != 3 || got[0] != "llama-2-7b-chat.Q4_K_M.gguf" || got[2] != "llama-2-7b-chat.Q8_0.gguf" {
		t.Fatalf("expected three gguf files, got %v", got)
	}

	got, err = ExpandFiles([]string{"llama-2-7b-chat.Q5_K_M.gguf", "config.json"}, repo)
	if err != nil {
		t.Fatalf("exact: %v", err)
	}
	if len(got) != 2 || got[0] != "config.json" || got[1] != "llama-2-7b-chat.Q5_K_M.gguf" {
		t.Fatalf("expected sorted exact files, got %v", got)
	}

	got, err = ExpandFiles([]string{"**/*.onnx"}, repo)
	if err != nil {
		t.Fatalf("nested glob: %v", err)
	}
	if len(got) != 1 || got[0] != "onnx/model.onnx" {
		t.Fatalf("expected nested onnx, got %v", got)
	}
}

func TestExpandFiles_EmptyGlobHint(t *testing.T) {
	_, err := ExpandFiles([]string{"*.bin"}, []string{"model.gguf", "config.json"})
	if err == nil || !strings.Contains(err.Error(), "matched no files") {
		t.Fatalf("expected empty-glob hint, got %v", err)
	}
	if !strings.Contains(err.Error(), "spec.files") {
		t.Fatalf("expected spec.files hint, got %v", err)
	}
}

func TestExpandFiles_MissingExact(t *testing.T) {
	_, err := ExpandFiles([]string{"missing.gguf"}, []string{"model.gguf"})
	if err == nil || !strings.Contains(err.Error(), "not in the repository") {
		t.Fatalf("expected missing file error, got %v", err)
	}
}

func TestGuardMultiGGUF(t *testing.T) {
	if err := GuardMultiGGUF([]string{"model.gguf", "config.json"}); err != nil {
		t.Fatalf("single gguf should pass: %v", err)
	}
	if err := GuardMultiGGUF([]string{"config.json"}); err != nil {
		t.Fatalf("no gguf should pass: %v", err)
	}
	err := GuardMultiGGUF([]string{"a.gguf", "b.gguf", "config.json"})
	if err == nil || !errors.Is(err, ErrMultiGGUF) {
		t.Fatalf("expected multi-gguf error, got %v", err)
	}
	if !strings.Contains(err.Error(), "a.gguf") || !strings.Contains(err.Error(), "b.gguf") {
		t.Fatalf("expected listed gguf names, got %v", err)
	}
}

func TestValidateFileSelection(t *testing.T) {
	repo := []string{"a.gguf", "b.gguf", "config.json"}
	if err := ValidateFileSelection(repo, nil); err == nil || !errors.Is(err, ErrMultiGGUF) {
		t.Fatalf("empty files against multi-gguf: %v", err)
	}
	if err := ValidateFileSelection(repo, []string{"*.gguf"}); err != nil {
		t.Fatalf("glob should pass: %v", err)
	}
}

func TestSnapshotSidecars(t *testing.T) {
	repo := []string{
		"README.md",
		".gitattributes",
		"config.json",
		"tokenizer.json",
		"tokenizer_config.json",
		"vocab.json",
		"video_preprocessor_config.json",
		"model.safetensors.index.json",
		"model-00001-of-00002.safetensors",
		"model-00002-of-00002.safetensors",
	}
	got := snapshotSidecars(repo)
	joined := strings.Join(got, ",")
	if strings.Contains(joined, "README") || strings.Contains(joined, "model-00001") {
		t.Fatalf("snapshot sidecars should omit readme and shards, got %v", got)
	}
	if !containsAll(got, "config.json", "tokenizer.json", "model.safetensors.index.json") {
		t.Fatalf("expected config/tokenizer/index, got %v", got)
	}
}

func TestShardsFromIndex(t *testing.T) {
	raw, err := json.Marshal(weightIndexJSON{WeightMap: map[string]string{
		"layer.0": "model-00001-of-00002.safetensors",
		"layer.1": "model-00002-of-00002.safetensors",
	}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	repo := []string{"model.safetensors.index.json", "model-00001-of-00002.safetensors", "model-00002-of-00002.safetensors"}
	got, err := shardsFromIndex("model.safetensors.index.json", raw, repo)
	if err != nil {
		t.Fatalf("shards: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 shards, got %v", got)
	}
	_, err = shardsFromIndex("model.safetensors.index.json", raw, []string{"model.safetensors.index.json"})
	if err == nil || !strings.Contains(err.Error(), "not in the repository") {
		t.Fatalf("expected missing shard error, got %v", err)
	}
}

func containsAll(got []string, want ...string) bool {
	set := map[string]struct{}{}
	for _, name := range got {
		set[name] = struct{}{}
	}
	for _, name := range want {
		if _, ok := set[name]; !ok {
			return false
		}
	}
	return true
}
