package cdidevices

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pelletier/go-toml"
	"gopkg.in/yaml.v3"
)

// DefaultSpecDirs are the host paths containerd scans when CDI is enabled.
var DefaultSpecDirs = []string{"/etc/cdi", "/var/run/cdi"}

type cdiSpecFile struct {
	Kind    string          `json:"kind" yaml:"kind"`
	Devices []cdiSpecDevice `json:"devices" yaml:"devices"`
}

type cdiSpecDevice struct {
	Name string `json:"name" yaml:"name"`
}

// ListFromDirs returns unique sorted fully-qualified CDI device names from spec dirs.
// Missing directories and unreadable files are skipped.
func ListFromDirs(dirs []string) []string {
	out := make([]string, 0)
	seen := map[string]struct{}{}
	for _, dir := range uniqueDirs(dirs) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			for _, name := range namesFromSpecFile(path) {
				if _, ok := seen[name]; ok {
					continue
				}
				seen[name] = struct{}{}
				out = append(out, name)
			}
		}
	}
	slices.Sort(out)
	if out == nil {
		return []string{}
	}
	return out
}

// ExtraSpecDirsFromConfigD reads extra cdi_spec_dirs from generated containerd config.d files.
func ExtraSpecDirsFromConfigD(configDDir string) []string {
	configDDir = strings.TrimSpace(configDDir)
	if configDDir == "" {
		return nil
	}
	entries, err := os.ReadDir(configDDir)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".toml") {
			continue
		}
		tree, loadErr := toml.LoadFile(filepath.Join(configDDir, name))
		if loadErr != nil || tree == nil {
			continue
		}
		collectCDISpecDirs(tree.ToMap(), &dirs)
	}
	return uniqueDirs(dirs)
}

func namesFromSpecFile(path string) []string {
	raw, err := os.ReadFile(path) // #nosec G304 -- host CDI spec dirs are operator-controlled
	if err != nil || len(raw) == 0 {
		return nil
	}
	var spec cdiSpecFile
	if err := json.Unmarshal(raw, &spec); err != nil {
		if yamlErr := yaml.Unmarshal(raw, &spec); yamlErr != nil {
			return nil
		}
	}
	kind := strings.TrimSpace(spec.Kind)
	if kind == "" {
		return nil
	}
	names := make([]string, 0, len(spec.Devices))
	for _, device := range spec.Devices {
		deviceName := strings.TrimSpace(device.Name)
		if deviceName == "" {
			continue
		}
		names = append(names, kind+"="+deviceName)
	}
	return names
}

func collectCDISpecDirs(value any, out *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "cdi_spec_dirs" {
				*out = append(*out, asStrings(child)...)
				continue
			}
			collectCDISpecDirs(child, out)
		}
	case []any:
		for _, child := range typed {
			collectCDISpecDirs(child, out)
		}
	}
}

func asStrings(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			s, ok := item.(string)
			if !ok {
				continue
			}
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			out = append(out, s)
		}
		return out
	default:
		return nil
	}
}

func uniqueDirs(dirs []string) []string {
	if len(dirs) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		out = append(out, dir)
	}
	return out
}
