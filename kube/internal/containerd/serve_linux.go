//go:build linux && cgo

package containerd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/containerd/containerd/v2/cmd/containerd/command"
)

func serveChild(args []string) error {
	settings, err := ParseChildArgs(args)
	if err != nil {
		return err
	}
	for _, dir := range []string{settings.Root, settings.State, settings.CNIConfDir, filepath.Dir(settings.Socket)} {
		if err := os.MkdirAll(dir, 0o755); err != nil { // #nosec G301 -- containerd and CNI traverse these directories
			return fmt.Errorf("create containerd directory: %w", err)
		}
	}
	if err := WriteConfig(settings); err != nil {
		return err
	}
	app := command.App()
	argv := []string{
		"containerd",
		"--config", settings.ConfigFile,
		"--address", settings.Socket,
		"--root", settings.Root,
		"--state", settings.State,
		"--log-level", settings.LogLevel,
	}
	if err := app.Run(argv); err != nil {
		return fmt.Errorf("containerd: %w", err)
	}
	return nil
}
