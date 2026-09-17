package prune

import (
	"fmt"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/cli/run"
)

// ParseMode parses prune mode flags and positional arguments.
func ParseMode(args []string, usage string) (mode string, err error) {
	mode = ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help", "-?":
			return "", run.NewCLIError(run.CodeInvalidArgument, usage, nil)
		case "-m", "--mode":
			if i+1 >= len(args) {
				return "", run.NewCLIError(run.CodeInvalidArgument, "--mode requires dangling|containers|volumes|all", nil)
			}
			mode = strings.ToLower(strings.TrimSpace(args[i+1]))
			i++
		default:
			if strings.HasPrefix(args[i], "-") {
				return "", run.NewCLIError(run.CodeInvalidArgument, fmt.Sprintf("unknown flag %s", args[i]), nil)
			}
			if mode != "" {
				return "", run.NewCLIError(run.CodeInvalidArgument, "prune mode provided multiple times", nil)
			}
			mode = strings.ToLower(strings.TrimSpace(args[i]))
		}
	}
	if mode == "" {
		return "", nil
	}
	switch mode {
	case "dangling", "containers", "volumes", "all":
		return mode, nil
	default:
		return "", run.NewCLIError(run.CodeInvalidArgument, "prune mode must be dangling|containers|volumes|all", nil)
	}
}

// ParseImageMode parses image prune mode (dangling only).
func ParseImageMode(args []string, usage string) (mode string, err error) {
	mode, err = ParseMode(args, usage)
	if err != nil {
		return "", err
	}
	if mode == "" {
		return "", nil
	}
	if mode != "dangling" {
		return "", run.NewCLIError(run.CodeInvalidArgument, "image prune supports only dangling mode", nil)
	}
	return mode, nil
}

// ParseModelMode parses model prune mode (dangling only).
func ParseModelMode(args []string, usage string) (mode string, err error) {
	mode, err = ParseMode(args, usage)
	if err != nil {
		return "", err
	}
	if mode == "" {
		return "", nil
	}
	if mode != "dangling" {
		return "", run.NewCLIError(run.CodeInvalidArgument, "model prune supports only dangling mode", nil)
	}
	return mode, nil
}
