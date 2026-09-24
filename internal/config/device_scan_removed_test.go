package config

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDefaultYAMLOmitsDeviceScanFrequency(t *testing.T) {
	yamlConfig := createDefaultYamlConfigForLoader()
	data, err := yaml.Marshal(yamlConfig)
	if err != nil {
		t.Fatalf("marshal default yaml: %v", err)
	}
	text := string(data)
	for _, key := range []string{"deviceScanFrequency", "scanDevicesFreq", "scanDevicesFrequency"} {
		if strings.Contains(text, key) {
			t.Fatalf("default YAML must not contain %q:\n%s", key, text)
		}
	}
}

func TestSetConfigRejectsRemovedDeviceScanFrequency(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "config-device-scan-removed-*.yaml")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	content := `currentProfile: default
profiles:
  default:
    changeFrequency: "20"
`
	if _, err := tmpFile.WriteString(content); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatalf("close temp config: %v", err)
	}
	if err := LoadConfig(tmpFile.Name()); err != nil {
		t.Fatalf("load config: %v", err)
	}

	cfg := GetInstance()
	errors := cfg.SetConfig(map[string]any{"sd": 60})
	if _, ok := errors["sd"]; !ok {
		t.Fatalf("expected SetConfig to reject sd, got: %+v", errors)
	}
	if _, ok := ConfigParamMap["sd"]; ok {
		t.Fatal("ConfigParamMap must not include sd")
	}

	changed := cfg.FilterChangedConfigKeys(map[string]any{"sd": 60, "cf": 20})
	if _, ok := changed["sd"]; ok {
		t.Fatalf("FilterChangedConfigKeys must omit sd, got: %+v", changed)
	}
}

func TestLoadConfigLeavesUnknownDeviceScanYAMLKeyUnused(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "config-leftover-device-scan-*.yaml")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	content := `currentProfile: default
profiles:
  default:
    changeFrequency: "20"
    deviceScanFrequency: "60"
`
	if _, err := tmpFile.WriteString(content); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatalf("close temp config: %v", err)
	}
	if err := LoadConfig(tmpFile.Name()); err != nil {
		t.Fatalf("leftover YAML key must load like other unknown profile properties: %v", err)
	}
}
