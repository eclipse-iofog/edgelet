//go:build linux

package processmanager

import (
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestListVolumeTreeHolders_FlockFixture(t *testing.T) {
	diskDir := t.TempDir()
	dataDir := filepath.Join(diskDir, "volumes", "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir volume tree: %v", err)
	}
	flockPath := filepath.Join(dataDir, "status")
	f, err := os.OpenFile(flockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("create flock file: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })

	cmd := exec.Command(os.Args[0], "-test.run=TestVolumeHolderFlockHelper", "--")
	cmd.Env = append(os.Environ(),
		"VOLUME_HOLDER_HELPER=1",
		"VOLUME_HOLDER_PATH="+flockPath,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start flock helper: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})

	deadline := time.Now().Add(2 * time.Second)
	var holders []int
	for time.Now().Before(deadline) {
		holders, err = ListVolumeTreeHolders(diskDir)
		if err != nil {
			t.Fatalf("ListVolumeTreeHolders: %v", err)
		}
		if containsPID(holders, cmd.Process.Pid) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !containsPID(holders, cmd.Process.Pid) {
		t.Fatalf("expected helper pid %d among volume holders, got %v", cmd.Process.Pid, holders)
	}

	if err := killProcess(cmd.Process.Pid); err != nil {
		t.Fatalf("SIGKILL helper: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("helper did not exit after SIGKILL")
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		holders, err = ListVolumeTreeHolders(diskDir)
		if err != nil {
			t.Fatalf("ListVolumeTreeHolders after kill: %v", err)
		}
		if !containsPID(holders, cmd.Process.Pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected flock released after SIGKILL, still held by %v", holders)
}

func TestVolumeHolderFlockHelper(t *testing.T) {
	if os.Getenv("VOLUME_HOLDER_HELPER") != "1" {
		return
	}
	path := os.Getenv("VOLUME_HOLDER_PATH")
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open lock file: %v", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("lock file: %v", err)
	}
	// A timer keeps the non-cgo runtime from treating this process as deadlocked.
	signal.Ignore(syscall.SIGTERM)
	for {
		time.Sleep(time.Hour)
	}
}

func containsPID(pids []int, want int) bool {
	for _, pid := range pids {
		if pid == want {
			return true
		}
	}
	return false
}
