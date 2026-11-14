//go:build linux && cgo

package containerd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// StartChild re-execs this process as the containerd child.
// args are the flags after the child switch. The caller stops the process.
func StartChild(args []string, stdout, stderr io.Writer) (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve executable: %w", err)
	}
	cmd := exec.Command(exe, append([]string{ChildArg}, args...)...) // #nosec G204 -- re-exec this binary as the containerd child
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start containerd: %w", err)
	}
	return cmd, nil
}

// StopChild stops a process started by StartChild, including its group.
func StopChild(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
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
