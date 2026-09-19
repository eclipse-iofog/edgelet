package models

import (
	"errors"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func int64Val(v int64) *int64 { return &v }

func float64Val(v float64) *float64 { return &v }

func lookupFromMap(m map[string]string) ModelSourceLookup {
	return func(name string) (string, bool, error) {
		src, ok := m[name]
		return src, ok, nil
	}
}

func TestValidate_CatalogEmptyItemsNoBindPathRequired(t *testing.T) {
	doc := validLocalDeployManifestForTest("cat-empty")
	if err := doc.Validate(); err != nil {
		t.Fatalf("omitted models should pass: %v", err)
	}
	doc.Spec.Models = &ModelCatalog{Items: nil}
	if err := doc.Validate(); err != nil {
		t.Fatalf("empty items should pass without bindPath: %v", err)
	}
	doc.Spec.Models = &ModelCatalog{Items: []ModelCatalogItem{}}
	if err := doc.Validate(); err != nil {
		t.Fatalf("empty items slice should pass without bindPath: %v", err)
	}
}

func TestValidate_CatalogItemsWithoutBindPath(t *testing.T) {
	doc := validLocalDeployManifestForTest("cat-nobind")
	doc.Spec.Models = &ModelCatalog{Items: []ModelCatalogItem{{Name: "test-model"}}}
	err := doc.Validate()
	if err == nil || !strings.Contains(err.Error(), "bindPath is required") {
		t.Fatalf("expected bindPath required, got %v", err)
	}
}

func TestValidate_CatalogPermissionsDefaultRO(t *testing.T) {
	doc := validLocalDeployManifestForTest("cat-perm-default")
	doc.Spec.Models = &ModelCatalog{
		BindPath: "/models",
		Items:    []ModelCatalogItem{{Name: "test-model"}},
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if doc.Spec.Models.Permissions != ModelCatalogPermRO {
		t.Fatalf("expected default ro, got %q", doc.Spec.Models.Permissions)
	}
}

func TestValidate_CatalogPermissionsRW(t *testing.T) {
	doc := validLocalDeployManifestForTest("cat-perm-rw")
	doc.Spec.Models = &ModelCatalog{
		BindPath:    "/models",
		Permissions: ModelCatalogPermRW,
		Items:       []ModelCatalogItem{{Name: "test-model"}},
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("rw should be accepted: %v", err)
	}
}

func TestValidate_CatalogDuplicateItemName(t *testing.T) {
	doc := validLocalDeployManifestForTest("cat-dup")
	doc.Spec.Models = &ModelCatalog{
		BindPath: "/models",
		Items: []ModelCatalogItem{
			{Name: "test-model"},
			{Name: "test-model"},
		},
	}
	err := doc.Validate()
	if err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("expected duplicate name error, got %v", err)
	}
}

func TestValidate_CatalogBindPathCollidesWithVolumeAndTmpfs(t *testing.T) {
	t.Run("volume", func(t *testing.T) {
		doc := validLocalDeployManifestForTest("cat-vol")
		doc.Spec.Container.Volumes = []struct {
			HostDestination      string `yaml:"hostDestination" json:"hostDestination"`
			ContainerDestination string `yaml:"containerDestination" json:"containerDestination"`
			AccessMode           string `yaml:"accessMode,omitempty" json:"accessMode,omitempty"`
			Type                 string `yaml:"type,omitempty" json:"type,omitempty"`
			Scope                string `yaml:"scope,omitempty" json:"scope,omitempty"`
		}{{
			HostDestination:      "/var/lib/data",
			ContainerDestination: "/models",
			Type:                 "BIND",
		}}
		doc.Spec.Models = &ModelCatalog{
			BindPath: "/models",
			Items:    []ModelCatalogItem{{Name: "test-model"}},
		}
		err := doc.Validate()
		if err == nil || !strings.Contains(err.Error(), "collides") {
			t.Fatalf("expected volume collision, got %v", err)
		}
	})
	t.Run("tmpfs", func(t *testing.T) {
		doc := validLocalDeployManifestForTest("cat-tmpfs")
		doc.Spec.Container.Tmpfs = []TmpfsMount{{ContainerPath: "/models"}}
		doc.Spec.Models = &ModelCatalog{
			BindPath: "/models",
			Items:    []ModelCatalogItem{{Name: "test-model"}},
		}
		err := doc.Validate()
		if err == nil || !strings.Contains(err.Error(), "collides") {
			t.Fatalf("expected tmpfs collision, got %v", err)
		}
	})
}

func TestValidate_MemorySwapRequiresLimitUnlessMinusOne(t *testing.T) {
	doc := validLocalDeployManifestForTest("swap-need-limit")
	doc.Spec.Container.MemorySwap = int64Val(1024)
	err := doc.Validate()
	if err == nil || !strings.Contains(err.Error(), "memoryLimit") {
		t.Fatalf("expected memoryLimit required, got %v", err)
	}

	doc = validLocalDeployManifestForTest("swap-unlimited")
	doc.Spec.Container.MemorySwap = int64Val(-1)
	if err := doc.Validate(); err != nil {
		t.Fatalf("memorySwap -1 without limit should pass: %v", err)
	}
}

func TestValidate_RunAsUserColonWithGroup(t *testing.T) {
	doc := validLocalDeployManifestForTest("runas-colon")
	doc.Spec.Container.RunAsUser = "0:0"
	doc.Spec.Container.RunAsGroup = "0"
	err := doc.Validate()
	if err == nil || !strings.Contains(err.Error(), "runAsUser") {
		t.Fatalf("expected runAsUser/runAsGroup conflict, got %v", err)
	}
}

func TestValidate_DeviceHostPathMustBeUnderDev(t *testing.T) {
	doc := validLocalDeployManifestForTest("dev-bad")
	doc.Spec.Container.Devices = []DeviceMapping{{
		HostPath:      "/etc/passwd",
		ContainerPath: "/dev/passwd",
		Permissions:   "r",
	}}
	err := doc.Validate()
	if err == nil || !strings.Contains(err.Error(), "/dev") {
		t.Fatalf("expected /dev requirement, got %v", err)
	}
}

func TestValidate_SysctlAllowlistAndHostNetwork(t *testing.T) {
	doc := validLocalDeployManifestForTest("sysctl-bad")
	doc.Spec.Container.Sysctls = map[string]string{"net.ipv4.ip_forward": "1"}
	err := doc.Validate()
	if err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("expected allowlist error, got %v", err)
	}

	doc = validLocalDeployManifestForTest("sysctl-hostnet")
	doc.Spec.Container.HostNetworkMode = true
	doc.Spec.Container.Sysctls = map[string]string{"net.ipv4.tcp_syncookies": "1"}
	err = doc.Validate()
	if err == nil || !strings.Contains(err.Error(), "hostNetworkMode") {
		t.Fatalf("expected host network sysctl error, got %v", err)
	}
}

func TestValidate_SysctlHostIPC(t *testing.T) {
	doc := validLocalDeployManifestForTest("sysctl-hostipc")
	doc.Spec.Container.IpcMode = "host"
	doc.Spec.Container.Sysctls = map[string]string{"kernel.shm_rmid_forced": "1"}
	err := doc.Validate()
	if err == nil || !strings.Contains(err.Error(), "ipcMode") {
		t.Fatalf("expected host ipc sysctl error, got %v", err)
	}

	doc = validLocalDeployManifestForTest("sysctl-hostipc-net")
	doc.Spec.Container.IpcMode = "host"
	doc.Spec.Container.Sysctls = map[string]string{"net.ipv4.tcp_keepalive_time": "600"}
	if err := doc.Validate(); err != nil {
		t.Fatalf("net sysctl with host ipc should pass: %v", err)
	}

	doc = validLocalDeployManifestForTest("sysctl-hostpid")
	doc.Spec.Container.PidMode = "host"
	doc.Spec.Container.Sysctls = map[string]string{"net.ipv4.tcp_syncookies": "1"}
	if err := doc.Validate(); err != nil {
		t.Fatalf("net sysctl with host pid should pass: %v", err)
	}

	ipc := "host"
	ms := NewMicroservice("ms-hostipc", "alpine:3.19")
	ms.IpcMode = &ipc
	ms.Sysctls = map[string]string{"kernel.shm_rmid_forced": "1"}
	if err := ms.Validate(); err == nil || !strings.Contains(err.Error(), "ipcMode") {
		t.Fatalf("microservice host ipc sysctl error, got %v", err)
	}
}

func TestValidate_UlimitScalarRejected(t *testing.T) {
	raw := []byte(`
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: ulimit-scalar
spec:
  image: nginx:latest
  container:
    ulimits:
      nofile: 65536
`)
	doc := &LocalDeployManifest{}
	err := yaml.Unmarshal(raw, doc)
	if err == nil {
		t.Fatal("expected scalar ulimit to fail unmarshal")
	}
	if !strings.Contains(err.Error(), "soft and hard") {
		t.Fatalf("expected soft/hard error, got %v", err)
	}
}

func TestValidate_UlimitKeys(t *testing.T) {
	t.Run("omitted", func(t *testing.T) {
		doc := validLocalDeployManifestForTest("ulimit-omit")
		if err := doc.Validate(); err != nil {
			t.Fatalf("omitted ulimits should pass: %v", err)
		}
	})
	t.Run("subset", func(t *testing.T) {
		doc := validLocalDeployManifestForTest("ulimit-subset")
		doc.Spec.Container.Ulimits = map[string]Ulimit{
			"nofile": {Soft: 65536, Hard: 65536},
			"CPU":    {Soft: 3600, Hard: 3600},
		}
		if err := doc.Validate(); err != nil {
			t.Fatalf("known subset should pass: %v", err)
		}
		if _, ok := doc.Spec.Container.Ulimits["cpu"]; !ok {
			t.Fatal("expected cpu key normalized to lowercase")
		}
		if _, ok := doc.Spec.Container.Ulimits["CPU"]; ok {
			t.Fatal("uppercase cpu key should be rewritten")
		}
	})
	t.Run("unknown", func(t *testing.T) {
		doc := validLocalDeployManifestForTest("ulimit-unknown")
		doc.Spec.Container.Ulimits = map[string]Ulimit{
			"not-a-limit": {Soft: 1, Hard: 1},
		}
		err := doc.Validate()
		if err == nil || !strings.Contains(err.Error(), "not a known limit name") {
			t.Fatalf("expected unknown key error, got %v", err)
		}
	})
	t.Run("soft greater than hard", func(t *testing.T) {
		doc := validLocalDeployManifestForTest("ulimit-range")
		doc.Spec.Container.Ulimits = map[string]Ulimit{
			"nofile": {Soft: 200, Hard: 100},
		}
		err := doc.Validate()
		if err == nil || !strings.Contains(err.Error(), "soft must be <= hard") {
			t.Fatalf("expected soft<=hard error, got %v", err)
		}
	})
	t.Run("unlimited soft requires unlimited hard", func(t *testing.T) {
		doc := validLocalDeployManifestForTest("ulimit-soft-inf")
		doc.Spec.Container.Ulimits = map[string]Ulimit{
			"memlock": {Soft: -1, Hard: 1024},
		}
		err := doc.Validate()
		if err == nil || !strings.Contains(err.Error(), "hard must be -1") {
			t.Fatalf("expected unlimited soft/hard pairing, got %v", err)
		}
	})
	t.Run("unlimited hard allows finite soft", func(t *testing.T) {
		doc := validLocalDeployManifestForTest("ulimit-hard-inf")
		doc.Spec.Container.Ulimits = map[string]Ulimit{
			"memlock": {Soft: 1024, Hard: -1},
		}
		if err := doc.Validate(); err != nil {
			t.Fatalf("finite soft with unlimited hard should pass: %v", err)
		}
	})
}

func TestValidate_ControllerUlimitUnknownKey(t *testing.T) {
	ms := NewMicroservice("ms-ulimit", "nginx:latest")
	ms.Ulimits = map[string]Ulimit{"as": {Soft: 1, Hard: 1}}
	err := ms.Validate()
	if err == nil || !strings.Contains(err.Error(), "not a known limit name") {
		t.Fatalf("expected unknown key error on controller MS, got %v", err)
	}
}

func TestValidate_LocalMSManagedModelName(t *testing.T) {
	doc := validLocalDeployManifestForTest("src-local")
	doc.Spec.Models = &ModelCatalog{
		BindPath: "/models",
		Items:    []ModelCatalogItem{{Name: "fleet-model"}},
	}
	err := doc.ValidateWithModelSources(lookupFromMap(map[string]string{
		"fleet-model": ModelSourceManaged,
	}))
	var scope *ErrModelSourceScope
	if !errors.As(err, &scope) || scope.Name != "fleet-model" || scope.Want != ModelSourceLocal {
		t.Fatalf("expected local-only source error, got %v", err)
	}
}

func TestValidate_ControllerMSLocalOnlyModelName(t *testing.T) {
	ms := NewMicroservice("ms-1", "nginx:latest")
	ms.Models = &ModelCatalog{
		BindPath: "/models",
		Items:    []ModelCatalogItem{{Name: "operator-model"}},
	}
	err := ms.ValidateWithModelSources(ModelSourceManaged, lookupFromMap(map[string]string{
		"operator-model": ModelSourceLocal,
	}))
	var scope *ErrModelSourceScope
	if !errors.As(err, &scope) || scope.Name != "operator-model" || scope.Want != ModelSourceManaged {
		t.Fatalf("expected managed-only source error, got %v", err)
	}
}

func TestEntrypointCommandsOmitVsEmpty(t *testing.T) {
	omitted := &LocalDeployManifest{}
	if err := yaml.Unmarshal([]byte(`
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: omit-argv
spec:
  image: nginx:latest
`), omitted); err != nil {
		t.Fatalf("unmarshal omitted: %v", err)
	}
	if omitted.Spec.Container.Entrypoint != nil || omitted.Spec.Container.Commands != nil {
		t.Fatal("omitted entrypoint/commands must stay nil")
	}
	if !UsesImageDefault(omitted.Spec.Container.Entrypoint) || !UsesImageDefault(omitted.Spec.Container.Commands) {
		t.Fatal("omitted argv should use image default")
	}

	empty := &LocalDeployManifest{}
	if err := yaml.Unmarshal([]byte(`
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: empty-argv
spec:
  image: nginx:latest
  container:
    entrypoint: []
    commands: []
`), empty); err != nil {
		t.Fatalf("unmarshal empty: %v", err)
	}
	if empty.Spec.Container.Entrypoint == nil || empty.Spec.Container.Commands == nil {
		t.Fatal("explicit empty lists must be non-nil")
	}
	if len(*empty.Spec.Container.Entrypoint) != 0 || len(*empty.Spec.Container.Commands) != 0 {
		t.Fatal("explicit empty lists must have length 0")
	}
	if !UsesImageDefault(empty.Spec.Container.Entrypoint) || !UsesImageDefault(empty.Spec.Container.Commands) {
		t.Fatal("explicit empty argv should use image default")
	}
}

func TestValidate_SafeSysctlAccepted(t *testing.T) {
	doc := validLocalDeployManifestForTest("sysctl-ok")
	doc.Spec.Container.Sysctls = map[string]string{
		"kernel.shm_rmid_forced":      "1",
		"net.ipv4.tcp_keepalive_time": "600",
		"net.ipv4.tcp_rmem":           "4096 87380 6291456",
	}
	doc.Spec.Container.Cpus = float64Val(2.5)
	if err := doc.Validate(); err != nil {
		t.Fatalf("safe sysctl and cpus should pass: %v", err)
	}
}

func TestNeedsReadOnlyRootTmpfsWarning(t *testing.T) {
	if NeedsReadOnlyRootTmpfsWarning(false, nil) {
		t.Fatal("writable root should not warn")
	}
	if !NeedsReadOnlyRootTmpfsWarning(true, nil) {
		t.Fatal("read-only root without tmpfs should warn")
	}
	if NeedsReadOnlyRootTmpfsWarning(true, []TmpfsMount{{ContainerPath: "/tmp"}}) {
		t.Fatal("/tmp tmpfs should suppress warning")
	}
}
