package config

import (
	"strings"
	"testing"
)

func TestValidateProperty_CPULimitRange(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		value string
		ok    bool
	}{
		{"4", false},
		{"5", true},
		{"80", true},
		{"200", true},
		{"400", true},
		{"401", false},
	} {
		err := ValidateProperty("cpuLimit", tc.value)
		if tc.ok && err != nil {
			t.Fatalf("cpuLimit=%q: expected ok, got %v", tc.value, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("cpuLimit=%q: expected error", tc.value)
		}
		if !tc.ok && err != nil && !strings.Contains(err.Error(), "400") {
			t.Fatalf("cpuLimit=%q: unexpected error: %v", tc.value, err)
		}
	}
}

func TestValidateConfig_CPULimitMax(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		DiskLimit:                       10,
		MemoryLimit:                     4096,
		CPULimit:                        200,
		LogLimit:                        1,
		LogFileCount:                    10,
		LogLevel:                        "INFO",
		StatusFrequency:                 10,
		ChangeFrequency:                 10,
		ContainerEngine:                 "docker",
		ContainerEngineURL:              "unix:///var/run/docker.sock",
		ShutdownGracePeriodSeconds:      90,
		ControllerRequestTimeoutSeconds: 30,
		ControllerPingTimeoutSeconds:    30,
	}
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("CPULimit 200 should validate on docker config: %v", err)
	}

	cfg.CPULimit = 401
	if err := ValidateConfig(cfg); err == nil {
		t.Fatal("CPULimit 401 should fail validation")
	}
}
