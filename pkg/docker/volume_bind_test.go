package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/moby/moby/api/types/mount"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "edgelet-docker-volumes-")
	if err != nil {
		panic(err)
	}
	cfg := config.GetInstance()
	orig := cfg.DiskDirectory
	cfg.DiskDirectory = dir
	m.Run()
	cfg.DiskDirectory = orig
	_ = os.RemoveAll(dir)
}

func TestBuildVolumeBindsAndMounts_PrivateVolumeIsPerUUIDBind(t *testing.T) {
	uuid := "ms-private"
	name := "mydata"
	binds, mounts, err := buildVolumeBindsAndMounts([]*models.VolumeMapping{
		models.NewVolumeMapping(name, "/data", "rw", models.VolumeMappingTypeVolume),
	}, uuid, nil)
	if err != nil {
		t.Fatalf("build volume binds: %v", err)
	}
	if len(binds) != 0 {
		t.Fatalf("private VOLUME must not use a named volume bind, got %v", binds)
	}
	if len(mounts) != 1 || mounts[0].Type != mount.TypeBind {
		t.Fatalf("expected one bind mount, got %#v", mounts)
	}
	want := filepath.Join("volumes", "data", uuid, name)
	if !strings.Contains(mounts[0].Source, want) {
		t.Fatalf("private bind source %q does not contain %q", mounts[0].Source, want)
	}
	if mounts[0].Source == name {
		t.Fatal("private VOLUME must not be a node-global named volume")
	}
}

func TestBuildVolumeBindsAndMounts_SharedVolumeUsesSharedBind(t *testing.T) {
	name := "mydata"
	vm := models.NewVolumeMapping(name, "/data", "rw", models.VolumeMappingTypeVolume)
	vm.Scope = models.VolumeScopeShared
	binds, mounts, err := buildVolumeBindsAndMounts([]*models.VolumeMapping{vm}, "ms-shared-a", nil)
	if err != nil {
		t.Fatalf("build volume binds: %v", err)
	}
	if len(binds) != 0 {
		t.Fatalf("shared VOLUME must not use a named volume bind, got %v", binds)
	}
	if len(mounts) != 1 || mounts[0].Type != mount.TypeBind {
		t.Fatalf("expected one bind mount, got %#v", mounts)
	}
	want := filepath.Join("volumes", "shared", name)
	if !strings.Contains(mounts[0].Source, want) {
		t.Fatalf("shared bind source %q does not contain %q", mounts[0].Source, want)
	}

	_, mountsB, err := buildVolumeBindsAndMounts([]*models.VolumeMapping{vm}, "ms-shared-b", nil)
	if err != nil {
		t.Fatal(err)
	}
	if mounts[0].Source != mountsB[0].Source {
		t.Fatalf("local and controller consumers must share bind source, got %q vs %q", mounts[0].Source, mountsB[0].Source)
	}
}

func TestBuildVolumeBindsAndMounts_BindKeepsOperatorPath(t *testing.T) {
	host := "/opt/operator-data"
	binds, mounts, err := buildVolumeBindsAndMounts([]*models.VolumeMapping{
		models.NewVolumeMapping(host, "/data", "rw", models.VolumeMappingTypeBind),
	}, "ms-bind", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 0 {
		t.Fatalf("BIND must not be rewritten as a managed mount, got %#v", mounts)
	}
	if len(binds) != 1 || !strings.HasPrefix(binds[0], host+":") {
		t.Fatalf("BIND must keep the operator path, got %v", binds)
	}
}
