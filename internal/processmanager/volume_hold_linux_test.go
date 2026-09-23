//go:build linux

package processmanager

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

func TestReconcile_AddWaitsForFlockThenProceeds(t *testing.T) {
	withRestartClock(t)
	pm, ms, eng := newVolumeHoldPM(t, "ms-flock-add")
	eng.workload = nil
	ms.VolumeMappings = []*models.VolumeMapping{
		models.NewVolumeMapping("data", "/var/lib/data", "rw", models.VolumeMappingTypeVolume),
	}
	presetRuntimeState(ms.MicroserviceUUID, models.MicroserviceStateCreated)

	flockPath := filepath.Join(pm.catalogDiskDirectory, "volumes", "data", ms.MicroserviceUUID, "data", "status")
	if err := os.MkdirAll(filepath.Dir(flockPath), 0o755); err != nil {
		t.Fatalf("mkdir volume: %v", err)
	}
	if err := os.WriteFile(flockPath, nil, 0o600); err != nil {
		t.Fatalf("create status file: %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestVolumeHolderFlockHelper", "--")
	cmd.Env = append(os.Environ(),
		"VOLUME_HOLDER_HELPER=1",
		"VOLUME_HOLDER_PATH="+flockPath,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start flock helper: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})

	volumeDir := filepath.Dir(flockPath)
	if !waitForVolumeHolder(t, volumeDir, cmd.Process.Pid, true) {
		t.Fatal("helper did not hold the volume status file")
	}

	stats := &reconcileCycleStats{}
	pm.handleLatestMicroservices(stats)
	if stats.scheduledAdd != 0 || len(drainTasks(pm.taskQueue)) != 0 {
		t.Fatal("create must not be enqueued while a host process flocks the volume")
	}
	assertVolumeWait(t, ms.MicroserviceUUID, models.MicroserviceStateCreated)

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("stop helper: %v", err)
	}
	_ = cmd.Wait()
	if !waitForVolumeHolder(t, volumeDir, cmd.Process.Pid, false) {
		t.Fatal("volume stayed held after the helper exited")
	}

	stats = &reconcileCycleStats{}
	pm.handleLatestMicroservices(stats)
	tasks := drainTasks(pm.taskQueue)
	if stats.scheduledAdd != 1 || len(tasks) != 1 || tasks[0].Action != TaskActionAdd {
		t.Fatalf("create must proceed after the holder exits, stats=%+v tasks=%d", stats, len(tasks))
	}
}

func waitForVolumeHolder(t *testing.T, volumeDir string, pid int, wantHeld bool) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		holders, err := ListVolumePathHolders([]string{volumeDir})
		if err != nil {
			t.Fatalf("scan holders: %v", err)
		}
		held := containsPID(holders, pid)
		if held == wantHeld {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
