package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

func mergeFile(srv *Server, path string) error {
	body, err := os.ReadFile(path) // #nosec G304 -- path is the config file or a drop-in
	if err != nil {
		return fmt.Errorf("read config %s: %w", path, err)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	var doc map[string]any
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("read config %s: %w", path, err)
	}
	if doc == nil {
		return nil
	}
	keys := make([]string, 0, len(doc))
	for key := range doc {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if err := apply(srv, path, key, doc[key]); err != nil {
			return err
		}
	}
	return nil
}

func dropIns(configFile string) ([]string, error) {
	dir := configFile + ".d"
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read config directory %s: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") {
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	paths := make([]string, len(names))
	for i, name := range names {
		paths[i] = filepath.Join(dir, name)
	}
	return paths, nil
}
