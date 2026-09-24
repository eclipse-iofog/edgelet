package statusreporter

import (
	"errors"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/models"
)

func TestGetStatusReport_IncludesAvailableNetworkInterfacesAfterSystemTotalCPU(t *testing.T) {
	report := GetInstance().GetStatusReport()

	totalCPUIdx := strings.Index(report, "System Total CPU")
	availableInterfacesIdx := strings.Index(report, "Available Network Interfaces")
	if totalCPUIdx == -1 {
		t.Fatalf("expected System Total CPU line in report, got:\n%s", report)
	}
	if availableInterfacesIdx == -1 {
		t.Fatalf("expected Available Network Interfaces line in report, got:\n%s", report)
	}
	if availableInterfacesIdx < totalCPUIdx {
		t.Fatalf("expected Available Network Interfaces after System Total CPU, got:\n%s", report)
	}
}

func TestGetStatusReport_IncludesAvailableRuntimes(t *testing.T) {
	cfg := config.GetInstance()
	originalEngine := cfg.ContainerEngine
	cfg.ContainerEngine = "docker"
	t.Cleanup(func() {
		cfg.ContainerEngine = originalEngine
	})

	report := GetInstance().GetStatusReport()
	if !strings.Contains(report, "Available Runtimes          : docker") {
		t.Fatalf("expected available runtimes line in report, got:\n%s", report)
	}
	runtimesIdx := strings.Index(report, "Available Runtimes")
	classesIdx := strings.Index(report, "Runtime Classes")
	cdiIdx := strings.Index(report, "Available CDI Devices")
	if runtimesIdx == -1 || classesIdx < runtimesIdx || cdiIdx < classesIdx {
		t.Fatalf("expected runtime classes and CDI after available runtimes, got:\n%s", report)
	}
}

func TestGetAvailableRuntimes_DeterministicByEngineAndEmbeddedMode(t *testing.T) {
	originalLister := listRuntimeClassesForStatus
	originalExternal := listExternalRuntimesForStatus
	originalCatalog := listCatalogRuntimesForStatus
	t.Cleanup(func() {
		listRuntimeClassesForStatus = originalLister
		listExternalRuntimesForStatus = originalExternal
		listCatalogRuntimesForStatus = originalCatalog
	})

	listExternalRuntimesForStatus = func(engineName string) ([]string, error) {
		switch engineName {
		case "docker":
			return []string{"runc", "crun", "runc"}, nil
		case "podman":
			return []string{"crun", "runc", "crun"}, nil
		default:
			return nil, nil
		}
	}
	listRuntimeClassesForStatus = func() ([]*models.LocalRuntimeClass, error) {
		return []*models.LocalRuntimeClass{
			{Name: "edgelet-wasmtime", RuntimeName: "edgelet-wasmtime"},
			{Name: "spin", RuntimeName: "spin"},
		}, nil
	}
	listCatalogRuntimesForStatus = func() []string {
		return []string{"spin", "runc"}
	}

	runtimes := getAvailableRuntimesForEngine("docker", false)
	if strings.Join(runtimes, ",") != "crun,runc" {
		t.Fatalf("expected docker runtime list, got: %v", runtimes)
	}

	runtimes = getAvailableRuntimesForEngine("podman", false)
	if strings.Join(runtimes, ",") != "crun,runc" {
		t.Fatalf("expected podman runtime list, got: %v", runtimes)
	}

	runtimes = getAvailableRuntimesForEngine("edgelet", false)
	if strings.Join(runtimes, ",") != "crun" {
		t.Fatalf("expected baseline edgelet runtimes without embedded mode, got: %v", runtimes)
	}

	runtimes = getAvailableRuntimesForEngine("edgelet", true)
	if strings.Join(runtimes, ",") != "crun,edgelet-wasmtime,runc,spin" {
		t.Fatalf("expected embedded edgelet runtimes with runtime classes and catalog, got: %v", runtimes)
	}
}

func TestGetAppliedRuntimeClasses_SortedAppliedOnly(t *testing.T) {
	cfg := config.GetInstance()
	originalEngine := cfg.ContainerEngine
	originalLister := listRuntimeClassesForStatus
	t.Cleanup(func() {
		cfg.ContainerEngine = originalEngine
		listRuntimeClassesForStatus = originalLister
	})

	listRuntimeClassesForStatus = func() ([]*models.LocalRuntimeClass, error) {
		return []*models.LocalRuntimeClass{
			{Name: "spin", Handler: "spin", Source: models.RuntimeClassSourceManaged},
			{Name: "nvidia", Handler: "nvidia", Source: models.RuntimeClassSourceLocal},
		}, nil
	}

	cfg.ContainerEngine = "docker"
	if got := GetAppliedRuntimeClasses(); len(got) != 0 {
		t.Fatalf("docker classes want [], got %#v", got)
	}

	cfg.ContainerEngine = "edgelet"
	got := GetAppliedRuntimeClasses()
	if len(got) != 2 || got[0].Name != "nvidia" || got[0].Source != models.RuntimeClassSourceLocal {
		t.Fatalf("expected nvidia local first, got %#v", got)
	}
	if got[1].Name != "spin" || got[1].Source != models.RuntimeClassSourceManaged {
		t.Fatalf("expected spin managed second, got %#v", got)
	}
}

func TestGetAvailableCDIDevices_DockerEmpty(t *testing.T) {
	cfg := config.GetInstance()
	originalEngine := cfg.ContainerEngine
	cfg.ContainerEngine = "docker"
	t.Cleanup(func() { cfg.ContainerEngine = originalEngine })
	got := GetAvailableCDIDevices()
	if got == nil || len(got) != 0 {
		t.Fatalf("expected empty CDI list for docker, got %#v", got)
	}
}

func TestGetAvailableRuntimes_ExternalEngineFallbackOnInfoErrorOrEmpty(t *testing.T) {
	originalExternal := listExternalRuntimesForStatus
	t.Cleanup(func() {
		listExternalRuntimesForStatus = originalExternal
	})

	listExternalRuntimesForStatus = func(engineName string) ([]string, error) {
		if engineName == "docker" {
			return nil, errors.New("daemon unavailable")
		}
		return []string{}, nil
	}

	runtimes := getAvailableRuntimesForEngine("docker", false)
	if strings.Join(runtimes, ",") != "docker" {
		t.Fatalf("expected docker fallback runtime list, got: %v", runtimes)
	}

	runtimes = getAvailableRuntimesForEngine("podman", false)
	if strings.Join(runtimes, ",") != "podman" {
		t.Fatalf("expected podman fallback runtime list, got: %v", runtimes)
	}
}

func TestDaemonOperational(t *testing.T) {
	sr := GetInstance()
	for _, tc := range []struct {
		status models.ModuleStatus
		want   bool
	}{
		{models.ModuleStatusStarting, false},
		{models.ModuleStatusRunning, true},
		{models.ModuleStatusWarning, true},
		{models.ModuleStatusStopped, false},
	} {
		sr.UpdateSupervisorStatus(func(s *models.SupervisorStatus) {
			s.SetDaemonStatus(tc.status)
		})
		if got := sr.DaemonOperational(); got != tc.want {
			t.Fatalf("status=%s got=%v want=%v", tc.status, got, tc.want)
		}
	}
}
