package hf

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// ErrMultiGGUF is returned when a repository has more than one GGUF file
// and spec.files is empty.
var ErrMultiGGUF = errors.New("multiple GGUF files require an explicit spec.files list")

var snapshotSidecarNames = map[string]struct{}{
	"config.json":                    {},
	"generation_config.json":         {},
	"tokenizer.json":                 {},
	"tokenizer_config.json":          {},
	"special_tokens_map.json":        {},
	"vocab.json":                     {},
	"merges.txt":                     {},
	"added_tokens.json":              {},
	"preprocessor_config.json":       {},
	"video_preprocessor_config.json": {},
	"chat_template.json":             {},
	"chat_template.jinja":            {},
	"tokenizer.model":                {},
	"sentencepiece.bpe.model":        {},
	"spiece.model":                   {},
}

type weightIndexJSON struct {
	WeightMap map[string]string `json:"weight_map"`
}

// GGUFPaths returns repository-relative paths that end in .gguf.
func GGUFPaths(repoFiles []string) []string {
	var out []string
	for _, name := range repoFiles {
		if strings.EqualFold(path.Ext(name), ".gguf") {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// GuardMultiGGUF errors when more than one GGUF is present and the caller
// did not select files explicitly.
func GuardMultiGGUF(repoFiles []string) error {
	ggufs := GGUFPaths(repoFiles)
	if len(ggufs) <= 1 {
		return nil
	}
	return fmt.Errorf("%w: found %s", ErrMultiGGUF, formatPathList(ggufs))
}

// ExpandFiles expands spec.files against a repository listing.
// Exact paths must exist; globs that match nothing return an error with a hint.
func ExpandFiles(patterns, repoFiles []string) ([]string, error) {
	fileSet := make(map[string]struct{}, len(repoFiles))
	for _, name := range repoFiles {
		name = normalizeRepoPath(name)
		if name != "" {
			fileSet[name] = struct{}{}
		}
	}
	seen := make(map[string]struct{})
	var out []string
	for _, pattern := range patterns {
		pattern = normalizeRepoPath(pattern)
		if pattern == "" {
			continue
		}
		if isGlob(pattern) {
			matches := matchGlob(pattern, repoFiles)
			if len(matches) == 0 {
				return nil, fmt.Errorf("glob %q matched no files; set spec.files to exact paths or a pattern that matches the repository", pattern)
			}
			for _, match := range matches {
				if _, ok := seen[match]; ok {
					continue
				}
				seen[match] = struct{}{}
				out = append(out, match)
			}
			continue
		}
		if _, ok := fileSet[pattern]; !ok {
			return nil, fmt.Errorf("file %q is not in the repository", pattern)
		}
		if _, ok := seen[pattern]; ok {
			continue
		}
		seen[pattern] = struct{}{}
		out = append(out, pattern)
	}
	slices.Sort(out)
	return out, nil
}

// ValidateFileSelection checks Hub file-selection rules against a listing.
// An empty requested list triggers the multi-GGUF guard; otherwise globs and
// exact paths are expanded.
func ValidateFileSelection(repoFiles, requested []string) error {
	if !hasRequestedFiles(requested) {
		return GuardMultiGGUF(repoFiles)
	}
	_, err := ExpandFiles(requested, repoFiles)
	return err
}

func hasRequestedFiles(requested []string) bool {
	for _, name := range requested {
		if normalizeRepoPath(name) != "" {
			return true
		}
	}
	return false
}

func snapshotSidecars(repoFiles []string) []string {
	var out []string
	hasIndex := false
	for _, name := range repoFiles {
		name = normalizeRepoPath(name)
		if name == "" {
			continue
		}
		base := path.Base(name)
		if strings.HasSuffix(base, ".index.json") {
			hasIndex = true
			out = append(out, name)
			continue
		}
		if _, ok := snapshotSidecarNames[base]; ok {
			out = append(out, name)
		}
	}
	if !hasIndex {
		out = append(out, standaloneWeights(repoFiles)...)
	}
	ggufs := GGUFPaths(repoFiles)
	if len(ggufs) == 1 {
		out = append(out, ggufs[0])
	}
	return uniqueSorted(out)
}

func standaloneWeights(repoFiles []string) []string {
	var out []string
	for _, name := range repoFiles {
		name = normalizeRepoPath(name)
		if name == "" {
			continue
		}
		base := path.Base(name)
		switch {
		case strings.HasSuffix(base, ".safetensors"):
			out = append(out, name)
		case strings.HasSuffix(base, ".onnx"):
			out = append(out, name)
		case strings.HasSuffix(base, ".bin") && (strings.Contains(base, "pytorch") || strings.HasPrefix(base, "model")):
			out = append(out, name)
		}
	}
	return out
}

func shardsFromIndex(indexPath string, raw []byte, repoFiles []string) ([]string, error) {
	var parsed weightIndexJSON
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode weight index %s: %w", indexPath, err)
	}
	fileSet := make(map[string]struct{}, len(repoFiles))
	for _, name := range repoFiles {
		fileSet[normalizeRepoPath(name)] = struct{}{}
	}
	var shards []string
	for _, shard := range parsed.WeightMap {
		shard = normalizeRepoPath(shard)
		if shard == "" {
			continue
		}
		if _, ok := fileSet[shard]; !ok {
			return nil, fmt.Errorf("index %s references %q which is not in the repository", indexPath, shard)
		}
		shards = append(shards, shard)
	}
	return uniqueSorted(shards), nil
}

func matchGlob(pattern string, repoFiles []string) []string {
	var out []string
	for _, name := range repoFiles {
		name = normalizeRepoPath(name)
		if name == "" {
			continue
		}
		ok, err := doublestar.Match(pattern, name)
		if err != nil || !ok {
			continue
		}
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

func isGlob(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[{")
}

func normalizeRepoPath(name string) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	name = strings.TrimPrefix(name, "./")
	return strings.TrimPrefix(name, "/")
}

func uniqueSorted(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	var out []string
	for _, name := range in {
		name = normalizeRepoPath(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

func formatPathList(names []string) string {
	const maxListed = 8
	if len(names) <= maxListed {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s, ... (%d total)", strings.Join(names[:maxListed], ", "), len(names))
}
