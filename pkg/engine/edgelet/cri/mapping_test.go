//go:build linux

package cri

import (
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

func TestContainerConfigFromMicroserviceAppliesResourceLimits(t *testing.T) {
	mem := int64(64 * 1024 * 1024)
	cpus := "0-1"
	ms := &models.Microservice{
		MicroserviceUUID: "ms-1",
		MicroserviceName: "limits",
		ImageName:        "docker.io/library/alpine:3.19",
		MemoryLimit:      &mem,
		CPUSetCpus:       &cpus,
	}

	cfg, err := ContainerConfigFromMicroservice(ms, "host", nil, "0.log", "", "", "sandbox-1", "node-1")
	if err != nil {
		t.Fatalf("ContainerConfigFromMicroservice: %v", err)
	}
	if cfg.Linux == nil || cfg.Linux.Resources == nil {
		t.Fatal("expected linux resources block")
	}
	if cfg.Linux.Resources.MemoryLimitInBytes != mem {
		t.Fatalf("memory limit = %d want %d", cfg.Linux.Resources.MemoryLimitInBytes, mem)
	}
	if cfg.Linux.Resources.CpusetCpus != cpus {
		t.Fatalf("cpuset = %q want %q", cfg.Linux.Resources.CpusetCpus, cpus)
	}
}

func TestContainerConfigFromMicroserviceOmitsZeroMemoryLimit(t *testing.T) {
	zero := int64(0)
	ms := &models.Microservice{
		MicroserviceUUID: "ms-2",
		MicroserviceName: "no-limit",
		ImageName:        "docker.io/library/alpine:3.19",
		MemoryLimit:      &zero,
	}

	cfg, err := ContainerConfigFromMicroservice(ms, "host", nil, "0.log", "", "", "sandbox-2", "node-2")
	if err != nil {
		t.Fatalf("ContainerConfigFromMicroservice: %v", err)
	}
	if cfg.Linux.Resources.MemoryLimitInBytes != 0 {
		t.Fatalf("expected zero memory limit, got %d", cfg.Linux.Resources.MemoryLimitInBytes)
	}
}

func TestContainerConfigFromMicroservice_SpecMatrix(t *testing.T) {
	cpus := 2.5
	swapUnlimited := int64(-1)
	swapBytes := int64(512 * 1024 * 1024)
	reserve := int64(128 * 1024 * 1024)
	shm := int64(64 * 1024 * 1024)
	tmpSize := int64(64)
	user, group := "1000", "100"
	wd := "/app"
	entrypoint := []string{"/bin/custom"}
	commands := []string{"--serve"}

	ms := models.NewMicroservice("ms-1", "docker.io/library/alpine:3.19")
	ms.Entrypoint = &entrypoint
	ms.Commands = &commands
	ms.WorkingDir = &wd
	ms.RunAsUser = &user
	ms.RunAsGroup = &group
	ms.ReadOnlyRootFilesystem = true
	ms.Tmpfs = []models.TmpfsMount{{ContainerPath: "/tmp", Size: &tmpSize, Mode: "1777"}}
	ms.ShmSize = &shm
	ms.Cpus = &cpus
	ms.MemoryReservation = &reserve
	ms.MemorySwap = &swapUnlimited
	ms.Sysctls = map[string]string{"net.ipv4.tcp_syncookies": "1"}
	ms.Ulimits = map[string]models.Ulimit{"nofile": {Soft: 65536, Hard: 65536}}
	ms.Devices = []models.DeviceMapping{{HostPath: "/dev/null", ContainerPath: "/dev/null", Permissions: "rwm"}}
	ms.Models = &models.ModelCatalog{
		BindPath:    "/models",
		Permissions: "ro",
		Items:       []models.ModelCatalogItem{{Name: "test-model"}},
	}
	ms.Knowledge = &models.KnowledgeCatalog{
		BindPath:    "/knowledge",
		Permissions: "ro",
		Items:       []models.KnowledgeCatalogItem{{Name: "product-docs"}},
	}

	cfg, err := ContainerConfigFromMicroservice(ms, "host", nil, "0.log", "", "", "sandbox-1", "node-1")
	if err != nil {
		t.Fatalf("ContainerConfigFromMicroservice: %v", err)
	}
	if len(cfg.Command) != 1 || cfg.Command[0] != "/bin/custom" {
		t.Fatalf("command = %v", cfg.Command)
	}
	if len(cfg.Args) != 1 || cfg.Args[0] != "--serve" {
		t.Fatalf("args = %v", cfg.Args)
	}
	if cfg.WorkingDir != "/app" {
		t.Fatalf("workingDir = %q", cfg.WorkingDir)
	}
	sec := cfg.Linux.SecurityContext
	if sec.RunAsUser == nil || sec.RunAsUser.Value != 1000 {
		t.Fatalf("runAsUser = %+v", sec.RunAsUser)
	}
	if sec.RunAsGroup == nil || sec.RunAsGroup.Value != 100 {
		t.Fatalf("runAsGroup = %+v", sec.RunAsGroup)
	}
	if !sec.ReadonlyRootfs {
		t.Fatal("expected readonly root")
	}
	res := cfg.Linux.Resources
	if res.CpuPeriod != 100000 || res.CpuQuota != 250000 {
		t.Fatalf("cpu period/quota = %d/%d", res.CpuPeriod, res.CpuQuota)
	}
	if res.Unified["memory.low"] != "134217728" {
		t.Fatalf("memory.low = %q", res.Unified["memory.low"])
	}
	if res.MemorySwapLimitInBytes != -1 {
		t.Fatalf("memorySwap = %d", res.MemorySwapLimitInBytes)
	}
	if len(cfg.Devices) != 1 || cfg.Devices[0].HostPath != "/dev/null" {
		t.Fatalf("devices = %+v", cfg.Devices)
	}

	var catalogRO, knowledgeRO, tmpfs, shmMount bool
	for _, m := range cfg.Mounts {
		if m.ContainerPath == "/models" && m.Readonly {
			catalogRO = true
		}
		if m.ContainerPath == "/knowledge" && m.Readonly {
			knowledgeRO = true
		}
		if m.ContainerPath == "/tmp" {
			tmpfs = true
		}
		if m.ContainerPath == "/dev/shm" {
			shmMount = true
		}
	}
	if !catalogRO || !knowledgeRO || !tmpfs || !shmMount {
		t.Fatalf("mounts catalogRO=%v knowledgeRO=%v tmpfs=%v shm=%v mounts=%v", catalogRO, knowledgeRO, tmpfs, shmMount, cfg.Mounts)
	}

	pod := PodSandboxConfigFromMicroservice(ms, "host", "/logs", "node-1")
	if pod.Linux == nil || pod.Linux.Sysctls["net.ipv4.tcp_syncookies"] != "1" {
		t.Fatalf("sandbox sysctls = %+v", pod.Linux)
	}

	ms.MemorySwap = &swapBytes
	cfg2, err := ContainerConfigFromMicroservice(ms, "host", nil, "0.log", "", "", "sandbox-1", "node-1")
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.Linux.Resources.MemorySwapLimitInBytes != swapBytes {
		t.Fatalf("positive memorySwap = %d", cfg2.Linux.Resources.MemorySwapLimitInBytes)
	}

	ms.Models.Permissions = "rw"
	cfg3, err := ContainerConfigFromMicroservice(ms, "host", nil, "0.log", "", "", "sandbox-1", "node-1")
	if err != nil {
		t.Fatal(err)
	}
	foundRW := false
	for _, m := range cfg3.Mounts {
		if m.ContainerPath == "/models" && !m.Readonly {
			foundRW = true
		}
	}
	if !foundRW {
		t.Fatal("expected rw catalog bind")
	}
}

func TestContainerConfigFromMicroservice_OmitEntrypointCommands(t *testing.T) {
	ms := models.NewMicroservice("ms-omit", "docker.io/library/alpine:3.19")
	empty := []string{}
	ms.Entrypoint = &empty
	ms.Commands = &empty
	cfg, err := ContainerConfigFromMicroservice(ms, "host", nil, "0.log", "", "", "sandbox-omit", "node-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Command) != 0 {
		t.Fatalf("omitted entrypoint must not set Command, got %v", cfg.Command)
	}
	if len(cfg.Args) != 0 {
		t.Fatalf("empty commands must not set Args, got %v", cfg.Args)
	}
}
