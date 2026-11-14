// Package dispatch decides which fat-runtime command the thin binary execs.
package dispatch

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/eclipse-iofog/edgelet-kube/internal/bundle"
)

// ErrUsage is returned when the arguments do not name a runtime command.
var ErrUsage = errors.New("usage: edgelet-kube server|kubectl|ctr|crictl")

// Command is the exec the thin binary performs after extract.
type Command struct {
	// Args is the argv passed to the fat runtime. Args[0] is the invoked name.
	Args []string
	// DataDir is where the bundle is extracted.
	DataDir string
}

// Parse reads the data directory and the fat-runtime argv from args.
// A flag wins over envDataDir. An empty envDataDir uses the default data directory.
func Parse(args []string, envDataDir string) (Command, error) {
	if len(args) == 0 {
		return Command{}, ErrUsage
	}
	dataDir, err := dataDir(args, envDataDir)
	if err != nil {
		return Command{}, err
	}
	execArgs, err := execArgs(args)
	if err != nil {
		return Command{}, err
	}
	return Command{Args: execArgs, DataDir: dataDir}, nil
}

func dataDir(args []string, envDataDir string) (string, error) {
	dir := envDataDir
	if dir == "" {
		dir = bundle.DefaultDataDir
	}
	flag := "--" + bundle.DataDirFlag
	for i := 1; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == flag:
			if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
				return "", fmt.Errorf("%s requires a path", bundle.DataDirFlag)
			}
			dir = args[i+1]
			i++
		case strings.HasPrefix(arg, flag+"="):
			value := strings.TrimPrefix(arg, flag+"=")
			if value == "" {
				return "", fmt.Errorf("%s requires a path", bundle.DataDirFlag)
			}
			dir = value
		}
	}
	return dir, nil
}

func execArgs(args []string) ([]string, error) {
	base := filepath.Base(args[0])
	if bundle.Client(base) {
		return append([]string(nil), args...), nil
	}
	command, err := firstCommand(args)
	if err != nil {
		return nil, err
	}
	if command == bundle.ServerCommand {
		return append([]string(nil), args...), nil
	}
	if bundle.Client(command) {
		return clientArgs(args, command), nil
	}
	return nil, ErrUsage
}

func firstCommand(args []string) (string, error) {
	flag := "--" + bundle.DataDirFlag
	for i := 1; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			return "", ErrUsage
		case arg == flag:
			i++
		case strings.HasPrefix(arg, flag+"="):
		case strings.HasPrefix(arg, "-"):
		default:
			return arg, nil
		}
	}
	return "", ErrUsage
}

func clientArgs(args []string, name string) []string {
	out := []string{name}
	skipped := false
	for i := 1; i < len(args); i++ {
		if !skipped && args[i] == name {
			skipped = true
			continue
		}
		out = append(out, args[i])
	}
	return out
}
