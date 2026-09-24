package config

import (
	"os"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/buildmeta"
	"github.com/eclipse-iofog/edgelet/internal/constants"
)

func TestLoadConfig(t *testing.T) {
	// Create a temporary config file
	tmpFile, err := os.CreateTemp("", "test-config-*.yaml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	// Write test config
	testConfig := `currentProfile: default
profiles:
  default:
    diskLimit: "10"
    memoryLimit: "4096"
    cpuLimit: "80"
    logLevel: "INFO"
`
	if _, err := tmpFile.WriteString(testConfig); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}
	_ = tmpFile.Close()

	// Load config
	if err := LoadConfig(tmpFile.Name()); err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	cfg := GetInstance()
	if cfg == nil {
		t.Fatal("Config instance is nil")
	}

	// Verify some values
	if cfg.DiskLimit != 10.0 {
		t.Errorf("Expected DiskLimit to be 10.0, got %f", cfg.DiskLimit)
	}
	if cfg.MemoryLimit != 4096.0 {
		t.Errorf("Expected MemoryLimit to be 4096.0, got %f", cfg.MemoryLimit)
	}
}

func TestValidateConfig(t *testing.T) {
	cfg := GetInstance()
	cfg.DiskLimit = 10.0
	cfg.MemoryLimit = 4096.0
	cfg.CPULimit = 80.0
	cfg.LogLimit = 10.0
	cfg.LogFileCount = 10
	cfg.LogLevel = "INFO"
	cfg.StatusFrequency = 10
	cfg.ChangeFrequency = 20
	cfg.EdgeGuardFrequency = 0
	cfg.GPSScanFrequency = 60
	if buildmeta.HasEmbeddedEngine() {
		cfg.ContainerEngine = constants.EngineEdgelet
		cfg.ContainerEngineURL = constants.EdgeletEngineSocketURL()
	} else {
		cfg.ContainerEngine = constants.EngineDocker
		cfg.ContainerEngineURL = "unix:///var/run/docker.sock"
	}
	cfg.ShutdownGracePeriodSeconds = 90
	cfg.ControllerRequestTimeoutSeconds = 30
	cfg.ControllerPingTimeoutSeconds = 60

	if err := ValidateConfig(cfg); err != nil {
		t.Errorf("Expected valid config, got error: %v", err)
	}

	cfg.DiskLimit = 1024.0
	if err := ValidateConfig(cfg); err != nil {
		t.Errorf("Expected 1024 GiB disk limit to be valid, got error: %v", err)
	}

	// Test invalid config
	cfg.DiskLimit = 200000.0 // Invalid: exceeds max
	if err := ValidateConfig(cfg); err == nil {
		t.Error("Expected validation error for invalid disk limit")
	}
}

func TestSnakeToCamel(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"disk_limit", "diskLimit"},
		{"log_file_count", "logFileCount"},
		{"gps_scan_frequency", "gpsScanFrequency"},
		{"", ""},
		{"simple", "simple"},
	}

	for _, tt := range tests {
		result := snakeToCamel(tt.input)
		if result != tt.expected {
			t.Errorf("snakeToCamel(%q) = %q, expected %q", tt.input, result, tt.expected)
		}
	}
}
