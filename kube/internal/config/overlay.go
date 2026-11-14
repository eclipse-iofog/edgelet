package config

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// applyEnv overlays EDGELET_KUBE_* values. A set variable replaces the key.
// List append from the files has already run.
func applyEnv(srv *Server, lookupEnv func(string) (string, bool)) error {
	if lookupEnv == nil {
		return nil
	}
	for _, key := range knownKeys() {
		name := envName(key)
		raw, ok := lookupEnv(name)
		if !ok || raw == "" {
			continue
		}
		if err := assign(srv, name, key, raw); err != nil {
			return err
		}
	}
	return nil
}

// applyFlags overlays command-line flags. The flag replaces the key, including
// a value that came from the environment. Repeated list flags collect one
// element each and then replace the key.
func applyFlags(srv *Server, args []string) error {
	lists := map[string][]string{}
	scalars := map[string]string{}
	var order []string
	seen := map[string]bool{}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "--") {
			continue
		}
		key, raw, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		if key == "" {
			return fmt.Errorf("unknown config key %q", arg)
		}
		if rejected(key) {
			return fmt.Errorf("config key %q is not accepted", key)
		}
		kind, ok := keyKind[key]
		if !ok {
			return fmt.Errorf("unknown config key %q", key)
		}
		if !hasValue {
			next, err := flagValue(args, i, key, kind)
			if err != nil {
				return err
			}
			if next.consumed {
				i++
			}
			raw = next.raw
		}
		if !seen[key] {
			seen[key] = true
			order = append(order, key)
		}
		if kind == kindList {
			lists[key] = append(lists[key], raw)
			continue
		}
		scalars[key] = raw
	}

	for _, key := range order {
		if items, ok := lists[key]; ok {
			if err := assignList(srv, "--"+key, key, items); err != nil {
				return err
			}
			continue
		}
		if err := assign(srv, "--"+key, key, scalars[key]); err != nil {
			return err
		}
	}
	return nil
}

type takenFlag struct {
	raw      string
	consumed bool
}

func flagValue(args []string, i int, key string, kind valueKind) (takenFlag, error) {
	if kind == kindBool {
		if i+1 < len(args) && (args[i+1] == "true" || args[i+1] == "false") {
			return takenFlag{raw: args[i+1], consumed: true}, nil
		}
		return takenFlag{raw: "true"}, nil
	}
	if i+1 >= len(args) || args[i+1] == "--" || strings.HasPrefix(args[i+1], "-") {
		return takenFlag{}, fmt.Errorf("config key %q requires a value", key)
	}
	return takenFlag{raw: args[i+1], consumed: true}, nil
}

func assign(srv *Server, source, key, raw string) error {
	if key == "config" {
		raw = filepath.Clean(raw)
	}
	if keyKind[key] == kindList {
		return assignList(srv, source, key, []string{raw})
	}
	value, err := parseScalar(key, raw)
	if err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}
	return apply(srv, source, key, value)
}

func assignList(srv *Server, source, key string, items []string) error {
	values := make([]any, len(items))
	for i, item := range items {
		values[i] = item
	}
	return apply(srv, source, key, values)
}

func parseScalar(key, raw string) (any, error) {
	switch keyKind[key] {
	case kindBool:
		switch raw {
		case "true":
			return true, nil
		case "false":
			return false, nil
		default:
			return nil, fmt.Errorf("config key %q must be true or false", key)
		}
	case kindInt:
		number, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("config key %q must be an integer", key)
		}
		return number, nil
	default:
		return raw, nil
	}
}

func envName(key string) string {
	return "EDGELET_KUBE_" + strings.ToUpper(strings.ReplaceAll(key, "-", "_"))
}

func knownKeys() []string {
	keys := make([]string, 0, len(setters))
	for key := range setters {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
