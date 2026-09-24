package handlers

import (
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
)

func TestAugmentWithResourceMetrics(t *testing.T) {
	t.Cleanup(func() {
		statusreporter.GetInstance().UpdateResourceConsumptionManagerStatus(func(s *models.ResourceConsumptionManagerStatus) {
			*s = *models.NewResourceConsumptionManagerStatus()
		})
	})

	statusreporter.GetInstance().UpdateResourceConsumptionManagerStatus(func(s *models.ResourceConsumptionManagerStatus) {
		s.AgentCPUPercent = 3
		s.AgentMemoryMiB = 24.5
		s.EdgeletTotalCPUPercent = 5
		s.EdgeletTotalMemoryMiB = 24.5
		s.CPUUsage = 5
		s.MemoryUsage = 24.5
		s.DiskUsage = 0.28 / 1024
		s.SystemCpus = 4
		s.SystemOs = "linux"
		s.SystemOsVersion = "Ubuntu 22.04"
		s.SystemKernelVersion = "6.8.0-generic"
		s.SystemTotalMemory = 8_000_000_000
		s.TotalDiskSpace = 50_000_000_000
		s.AvailableMemory = 4_000_000_000
		s.AvailableDisk = 25_000_000_000
		s.TotalCPU = 12.5
	})

	m := map[string]any{
		"cpuUsage":              "legacy string",
		"agentCpuPercent":       99.0,
		"edgeletTotalMemoryMiB": 1.0,
	}
	augmentWithResourceMetrics(m)

	if _, ok := m["agentCpuPercent"]; ok {
		t.Fatal("expected legacy agentCpuPercent removed")
	}
	if m["agentCpu"] != 0.03 {
		t.Fatalf("agentCpu: got %v want 0.03", m["agentCpu"])
	}
	if m["edgeletStackCpu"] != 0.05 {
		t.Fatalf("edgeletStackCpu: got %v want 0.05", m["edgeletStackCpu"])
	}
	wantAgentMem := int64(24.5 * 1024 * 1024)
	if m["agentMemory"] != wantAgentMem {
		t.Fatalf("agentMemory: got %v want %v", m["agentMemory"], wantAgentMem)
	}
	if m["cpuUsage"] != 5.0 {
		t.Fatalf("cpuUsage: got %v want 5", m["cpuUsage"])
	}
	if m["systemCpus"] != 4 {
		t.Fatalf("systemCpus: got %v", m["systemCpus"])
	}
	if m["systemOs"] != "linux" {
		t.Fatalf("systemOs: got %v", m["systemOs"])
	}
	if m["systemOsVersion"] != "Ubuntu 22.04" {
		t.Fatalf("systemOsVersion: got %v", m["systemOsVersion"])
	}
	if m["systemKernelVersion"] != "6.8.0-generic" {
		t.Fatalf("systemKernelVersion: got %v", m["systemKernelVersion"])
	}
}
