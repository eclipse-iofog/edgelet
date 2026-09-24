package docker

import (
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/moby/moby/api/types/container"
)

func TestApplyWorkloadCreateConfig_SpecMatrix(t *testing.T) {
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

	ms := models.NewMicroservice("ms-1", "alpine:3.19")
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

	cfg := &container.Config{Image: ms.ImageName}
	hc := &container.HostConfig{}
	warnings := applyWorkloadCreateConfig(cfg, hc, ms, "/var/lib/edgelet")
	if len(warnings) != 0 {
		t.Fatalf("tmpfs at /tmp should suppress read-only warning, got %v", warnings)
	}

	if len(cfg.Entrypoint) != 1 || cfg.Entrypoint[0] != "/bin/custom" {
		t.Fatalf("entrypoint = %v", cfg.Entrypoint)
	}
	if len(cfg.Cmd) != 1 || cfg.Cmd[0] != "--serve" {
		t.Fatalf("cmd = %v", cfg.Cmd)
	}
	if cfg.WorkingDir != "/app" {
		t.Fatalf("workingDir = %q", cfg.WorkingDir)
	}
	if cfg.User != "1000:100" {
		t.Fatalf("user = %q", cfg.User)
	}
	if !hc.ReadonlyRootfs {
		t.Fatal("expected readonly root")
	}
	if hc.Tmpfs["/tmp"] != "size=64m,mode=1777" {
		t.Fatalf("tmpfs = %q", hc.Tmpfs["/tmp"])
	}
	if hc.ShmSize != shm {
		t.Fatalf("shm = %d", hc.ShmSize)
	}
	if hc.NanoCPUs != 2500000000 {
		t.Fatalf("NanoCPUs = %d", hc.NanoCPUs)
	}
	if hc.MemoryReservation != reserve {
		t.Fatalf("memoryReservation = %d", hc.MemoryReservation)
	}
	if hc.MemorySwap != -1 {
		t.Fatalf("memorySwap unlimited = %d", hc.MemorySwap)
	}
	if hc.Sysctls["net.ipv4.tcp_syncookies"] != "1" {
		t.Fatalf("sysctls = %v", hc.Sysctls)
	}
	if len(hc.Ulimits) != 1 || hc.Ulimits[0].Name != "nofile" {
		t.Fatalf("ulimits = %+v", hc.Ulimits)
	}
	if len(hc.Devices) != 1 || hc.Devices[0].PathOnHost != "/dev/null" {
		t.Fatalf("devices = %+v", hc.Devices)
	}

	foundCatalog := false
	foundKnowledge := false
	for _, b := range hc.Binds {
		if strings.Contains(b, "/volumes/microservices/ms-1/models") && strings.Contains(b, "/models") && strings.HasSuffix(b, ":ro") {
			foundCatalog = true
		}
		if strings.Contains(b, "/volumes/microservices/ms-1/knowledge") && strings.Contains(b, "/knowledge") && strings.HasSuffix(b, ":ro") {
			foundKnowledge = true
		}
	}
	if !foundCatalog || !foundKnowledge {
		t.Fatalf("catalog binds missing: %v", hc.Binds)
	}

	ms.MemorySwap = &swapBytes
	hc2 := &container.HostConfig{}
	applyWorkloadCreateConfig(&container.Config{}, hc2, ms, "/var/lib/edgelet")
	if hc2.MemorySwap != swapBytes {
		t.Fatalf("positive memorySwap = %d want %d", hc2.MemorySwap, swapBytes)
	}

	ms.Models.Permissions = "rw"
	hc3 := &container.HostConfig{}
	applyWorkloadCreateConfig(&container.Config{}, hc3, ms, "/var/lib/edgelet")
	foundRW := false
	for _, b := range hc3.Binds {
		if strings.Contains(b, "/models") && strings.HasSuffix(b, ":rw") {
			foundRW = true
		}
	}
	if !foundRW {
		t.Fatalf("rw catalog bind missing: %v", hc3.Binds)
	}
}

func TestApplyWorkloadCreateConfig_OmitEntrypointCommands(t *testing.T) {
	ms := models.NewMicroservice("ms-2", "alpine:3.19")
	empty := []string{}
	ms.Entrypoint = &empty
	ms.Commands = &empty
	cfg := &container.Config{Cmd: []string{"should-clear"}}
	hc := &container.HostConfig{}
	applyWorkloadCreateConfig(cfg, hc, ms, "")
	if cfg.Entrypoint != nil {
		t.Fatalf("omitted entrypoint must not set engine argv, got %v", cfg.Entrypoint)
	}
	if cfg.Cmd != nil {
		t.Fatalf("empty commands must not send argv, got %v", cfg.Cmd)
	}
}

func TestApplyWorkloadCreateConfig_ReadOnlyWithoutTmpWarns(t *testing.T) {
	ms := models.NewMicroservice("ms-3", "alpine:3.19")
	ms.ReadOnlyRootFilesystem = true
	warnings := applyWorkloadCreateConfig(&container.Config{}, &container.HostConfig{}, ms, "")
	if len(warnings) != 1 {
		t.Fatalf("expected read-only /tmp warning, got %v", warnings)
	}
}
