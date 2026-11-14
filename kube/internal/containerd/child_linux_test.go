//go:build linux && cgo

package containerd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet-kube/internal/paths"
	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	handled, err := MaybeRunChild(os.Args)
	if handled {
		if err != nil {
			fmt.Fprintf(os.Stderr, "containerd child: %v\n", err)
			unix.Exit(1)
		}
		unix.Exit(0)
	}
	m.Run()
}

func TestChildServesCRI(t *testing.T) {
	dir := filepath.Join("/tmp", fmt.Sprintf("edgelet-kube-child-%d", os.Getpid()))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	sock := filepath.Join(dir, "containerd.sock")
	logFile, err := os.Create(filepath.Join(dir, "child.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = logFile.Close()
	}()

	cmd := exec.Command(os.Args[0],
		ChildArg,
		"--address", sock,
		"--root", filepath.Join(dir, "root"),
		"--state", filepath.Join(dir, "state"),
		"--crun", filepath.Join(dir, "crun"),
		"--shim", filepath.Join(dir, "containerd-shim-runc-v2"),
		"--cni-bin-dir", filepath.Join(dir, "bin"),
		"--cni-conf-dir", filepath.Join(dir, "net.d"),
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopChild(cmd)
	})

	deadline := time.Now().Add(45 * time.Second)
	var version RuntimeInfo
	for {
		version, err = RuntimeVersion(t.Context(), sock)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			logText, _ := os.ReadFile(logFile.Name())
			t.Fatalf("cri version: %v\n%s", err, logText)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if version.RuntimeName == "" && version.RuntimeVersion == "" {
		t.Fatalf("empty cri version %+v", version)
	}

	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cmdline), ChildArg) {
		t.Fatalf("cmdline %q", cmdline)
	}
	listening, err := processListens(cmd.Process.Pid, 6443)
	if err != nil {
		t.Fatal(err)
	}
	if listening {
		t.Fatal("child listens on 6443")
	}

	t.Setenv(SocketEnv, sock)
	if err := RunCtr([]string{"version"}); err != nil {
		logText, _ := os.ReadFile(logFile.Name())
		t.Fatalf("ctr: %v\n%s", err, logText)
	}
	if err := RunCrictl([]string{"version"}); err != nil {
		t.Fatalf("crictl: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dir, "root", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "SystemdCgroup = true") || !strings.Contains(string(body), `default_runtime_name = "crun"`) {
		t.Fatalf("config:\n%s", body)
	}
}

func TestChildUsesRuntimePaths(t *testing.T) {
	for _, path := range []string{paths.ContainerdSocket, paths.ContainerdRoot, paths.ContainerdState, paths.CNIConfDir} {
		if _, err := os.Lstat(path); err == nil {
			t.Fatalf("%s already exists", path)
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	logFile, err := os.CreateTemp("", "edgelet-kube-child-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = logFile.Close()
		_ = os.Remove(logFile.Name())
	}()

	cmd := exec.Command(os.Args[0], ChildArg)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopChild(cmd)
		_ = os.RemoveAll(paths.ContainerdState)
		_ = os.RemoveAll(paths.ContainerdRoot)
		_ = os.RemoveAll(paths.CNIConfDir)
		removeEmpty(filepath.Dir(paths.CNIConfDir))
		removeEmpty(filepath.Dir(filepath.Dir(paths.CNIConfDir)))
		removeEmpty(filepath.Dir(paths.ContainerdRoot))
		removeEmpty(filepath.Dir(filepath.Dir(paths.ContainerdRoot)))
	})

	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(paths.ContainerdSocket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			logText, _ := os.ReadFile(logFile.Name())
			t.Fatalf("socket %s missing\n%s", paths.ContainerdSocket, logText)
		}
		time.Sleep(200 * time.Millisecond)
	}
	body, err := os.ReadFile(filepath.Join(paths.ContainerdRoot, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{paths.ContainerdRoot, paths.ContainerdSocket, paths.CNIConfDir, `snapshotter = "overlayfs"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("config missing %q\n%s", want, text)
		}
	}
	for _, blocked := range []string{"/opt/cni/bin", "/run/containerd/containerd.sock"} {
		if strings.Contains(text, blocked) {
			t.Fatalf("config contains %s\n%s", blocked, text)
		}
	}
}

func removeEmpty(path string) {
	_ = os.Remove(path)
}

func stopChild(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}
}

func processListens(pid, port int) (bool, error) {
	inodes, err := listenInodes(port)
	if err != nil || len(inodes) == 0 {
		return false, err
	}
	entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(fmt.Sprintf("/proc/%d/fd", pid), entry.Name()))
		if err != nil || !strings.HasPrefix(target, "socket:[") || !strings.HasSuffix(target, "]") {
			continue
		}
		inode := strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")
		if _, ok := inodes[inode]; ok {
			return true, nil
		}
	}
	return false, nil
}

func listenInodes(port int) (map[string]struct{}, error) {
	inodes := map[string]struct{}{}
	for _, name := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		body, err := os.ReadFile(name)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		lines := strings.Split(string(body), "\n")
		for _, line := range lines[1:] {
			fields := strings.Fields(line)
			if len(fields) < 10 || fields[3] != "0A" {
				continue
			}
			local := fields[1]
			colon := strings.LastIndex(local, ":")
			if colon < 0 {
				continue
			}
			var got int
			if _, err := fmt.Sscanf(local[colon+1:], "%x", &got); err != nil || got != port {
				continue
			}
			inodes[fields[9]] = struct{}{}
		}
	}
	return inodes, nil
}
