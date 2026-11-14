package control

import (
	"errors"

	goselinux "github.com/opencontainers/selinux/go-selinux"
)

// errNoSELinux is returned when selinux is enabled on a host without SELinux.
var errNoSELinux = errors.New("selinux is enabled on a host without SELinux")

// hostSELinux reports whether the host is running SELinux.
var hostSELinux = func() bool { return goselinux.GetEnabled() }

// CheckSELinux returns an error when selinux is requested on a host that
// has no SELinux. A false setting leaves the host unchanged. No policy is installed.
func CheckSELinux(enabled bool) error {
	if !enabled {
		return nil
	}
	if !hostSELinux() {
		return errNoSELinux
	}
	return nil
}
