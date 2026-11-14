// Package containerd runs containerd as a child of the fat runtime.
// crun is the CRI runtime, with SystemdCgroup enabled.
package containerd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/eclipse-iofog/edgelet-kube/internal/bundle"
	"github.com/eclipse-iofog/edgelet-kube/internal/paths"
)

const (
	// ChildArg is the re-exec switch for the process that serves containerd.
	ChildArg = "--edgelet-kube-containerd-child"

	// SocketEnv is the containerd socket for ctr and crictl when the command
	// line does not name one.
	SocketEnv = "EDGELET_KUBE_CONTAINERD_SOCKET"
)

// installedSocket is the containerd socket ctr and crictl use when the
// command line and SocketEnv are empty.
var installedSocket = paths.ContainerdSocket

// WantsClient reports whether args invoke ctr or crictl with a client command.
// A data-dir flag on its own leaves the process on the recognition path.
func WantsClient(name string, args []string) bool {
	if name != "ctr" && name != "crictl" {
		return false
	}
	return hasClientArgs(args)
}

func hasClientArgs(args []string) bool {
	flag := "--" + bundle.DataDirFlag
	for i := 1; i < len(args); i++ {
		switch {
		case args[i] == flag:
			i++
		case strings.HasPrefix(args[i], flag+"="):
		default:
			return true
		}
	}
	return false
}

// clientSocket resolves the containerd socket and the arguments left for the client.
func clientSocket(args []string) (string, []string, error) {
	explicit, rest, err := splitClientArgs(args)
	if err != nil {
		return "", nil, err
	}
	socket := explicit
	if socket == "" {
		socket = os.Getenv(SocketEnv)
	}
	if socket == "" {
		socket = installedSocket
	}
	socket = socketPath(socket)
	if socket == "" {
		return "", nil, errors.New("containerd socket is required")
	}
	if err := checkSocket(socket); err != nil {
		return "", nil, err
	}
	return socket, rest, nil
}

func splitClientArgs(args []string) (string, []string, error) {
	var explicit string
	var rest []string
	dataFlag := "--" + bundle.DataDirFlag
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == dataFlag:
			if i+1 >= len(args) || args[i+1] == "" {
				return "", nil, fmt.Errorf("%s requires a path", bundle.DataDirFlag)
			}
			i++
		case strings.HasPrefix(arg, dataFlag+"="):
			if strings.TrimPrefix(arg, dataFlag+"=") == "" {
				return "", nil, fmt.Errorf("%s requires a path", bundle.DataDirFlag)
			}
		case arg == "--address" || arg == "--runtime-endpoint" || arg == "--image-endpoint":
			if i+1 >= len(args) || args[i+1] == "" {
				return "", nil, fmt.Errorf("%s requires a value", strings.TrimPrefix(arg, "--"))
			}
			i++
			explicit = args[i]
		case strings.HasPrefix(arg, "--address="):
			explicit = strings.TrimPrefix(arg, "--address=")
		case strings.HasPrefix(arg, "--runtime-endpoint="):
			explicit = strings.TrimPrefix(arg, "--runtime-endpoint=")
		case strings.HasPrefix(arg, "--image-endpoint="):
			explicit = strings.TrimPrefix(arg, "--image-endpoint=")
		default:
			rest = append(rest, arg)
		}
	}
	if explicit == "" {
		return "", rest, nil
	}
	if socketPath(explicit) == "" {
		return "", nil, errors.New("containerd socket is required")
	}
	return explicit, rest, nil
}

func socketPath(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "unix://")
	return value
}

func checkSocket(socket string) error {
	cleaned := filepath.Clean(socket)
	switch cleaned {
	case "/run/containerd/containerd.sock", "/var/run/containerd/containerd.sock":
		return fmt.Errorf("unsupported containerd socket %s", socket)
	default:
		return nil
	}
}

func checkCNIBin(dir string) error {
	if filepath.Clean(dir) == "/opt/cni/bin" {
		return fmt.Errorf("unsupported cni binary directory %s", dir)
	}
	return nil
}
