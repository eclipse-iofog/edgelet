//go:build unix

package processmanager

import "syscall"

func killProcess(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}
