//go:build linux

package cri

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/containerapply"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"golang.org/x/sys/unix"
)

// PrepareHostTmpfs mounts sized tmpfs directories used as CRI bind sources.
func PrepareHostTmpfs(diskDir string, ms *models.Microservice) error {
	if ms == nil {
		return nil
	}
	for _, t := range ms.Tmpfs {
		p := strings.TrimSpace(t.ContainerPath)
		if p == "" {
			continue
		}
		host := containerapply.TmpfsHostDir(diskDir, ms.MicroserviceUUID, p)
		if err := os.MkdirAll(host, 0o755); err != nil { // #nosec G301 -- tmpfs mountpoint must be traversable
			return fmt.Errorf("create tmpfs directory %s: %w", host, err)
		}
		opts := containerapply.DockerTmpfsOptions(t)
		if err := unix.Mount("tmpfs", host, "tmpfs", uintptr(unix.MS_NOSUID|unix.MS_NOEXEC|unix.MS_NODEV), opts); err != nil {
			if err != unix.EBUSY {
				return fmt.Errorf("mount tmpfs at %s: %w", host, err)
			}
		}
	}
	if ms.ShmSize != nil && *ms.ShmSize > 0 {
		host := containerapply.ShmHostDir(diskDir, ms.MicroserviceUUID)
		if err := os.MkdirAll(host, 0o755); err != nil { // #nosec G301 -- /dev/shm mountpoint must be traversable
			return fmt.Errorf("create shm directory %s: %w", host, err)
		}
		opts := fmt.Sprintf("mode=1777,size=%d", *ms.ShmSize)
		if err := unix.Mount("shm", host, "tmpfs", uintptr(unix.MS_NOSUID|unix.MS_NOEXEC|unix.MS_NODEV), opts); err != nil {
			if err != unix.EBUSY {
				return fmt.Errorf("mount shm at %s: %w", host, err)
			}
		}
	}
	return nil
}

// ReleaseHostTmpfs unmounts per-microservice tmpfs and shm host directories.
func ReleaseHostTmpfs(diskDir, msUUID string) {
	msUUID = strings.TrimSpace(msUUID)
	if msUUID == "" {
		return
	}
	shm := containerapply.ShmHostDir(diskDir, msUUID)
	_ = unix.Unmount(shm, unix.MNT_DETACH)
	tmpfsRoot := filepath.Join(strings.TrimSpace(diskDir), "volumes", "microservices", msUUID, "tmpfs")
	entries, err := os.ReadDir(tmpfsRoot)
	if err != nil {
		return
	}
	for _, e := range entries {
		_ = unix.Unmount(filepath.Join(tmpfsRoot, e.Name()), unix.MNT_DETACH)
	}
}
