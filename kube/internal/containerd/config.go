package containerd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/eclipse-iofog/edgelet-kube/internal/config"
	"github.com/eclipse-iofog/edgelet-kube/internal/paths"
	"github.com/eclipse-iofog/edgelet-kube/internal/registries"
	"github.com/pelletier/go-toml"
)

// RuntimeName is the CRI runtime handler backed by crun.
const RuntimeName = "crun"

// Settings is the containerd child configuration.
type Settings struct {
	// Socket is the containerd gRPC socket.
	Socket string
	// Root is the containerd persistent directory.
	Root string
	// State is the containerd transient directory.
	State string
	// ConfigFile is the containerd config file to write.
	ConfigFile string
	// CrunBinary is the crun executable.
	CrunBinary string
	// ShimBinary is the containerd-shim-runc-v2 executable.
	ShimBinary string
	// CNIBinDir is the directory of CNI plugin binaries.
	CNIBinDir string
	// CNIConfDir is the directory of CNI configuration.
	CNIConfDir string
	// Snapshotter is the container snapshot driver.
	Snapshotter string
	// LogLevel is the containerd log level.
	LogLevel string
	// RegistryFile is the registries file. A missing file leaves containerd on upstream endpoints.
	RegistryFile string
	// RegistryDir is the containerd hosts directory.
	RegistryDir string
	// DisableDefaultEndpoint omits the upstream registry for names listed in RegistryFile.
	DisableDefaultEndpoint bool
	// PauseImage is the sandbox image.
	PauseImage string
}

// ParseChildArgs reads the containerd child command line.
// args[0] is the executable and args[1] is ChildArg.
func ParseChildArgs(args []string) (Settings, error) {
	if len(args) < 2 || args[1] != ChildArg {
		return Settings{}, fmt.Errorf("containerd child requires %s", ChildArg)
	}
	var settings Settings
	for i := 2; i < len(args); i++ {
		name, value, next, err := childFlag(args, i)
		if err != nil {
			return Settings{}, err
		}
		i = next
		switch name {
		case "address":
			settings.Socket = value
		case "root":
			settings.Root = value
		case "state":
			settings.State = value
		case "config":
			settings.ConfigFile = value
		case "crun":
			settings.CrunBinary = value
		case "shim":
			settings.ShimBinary = value
		case "cni-bin-dir":
			settings.CNIBinDir = value
		case "cni-conf-dir":
			settings.CNIConfDir = value
		case "snapshotter":
			settings.Snapshotter = value
		case "log-level":
			settings.LogLevel = value
		case "private-registry":
			settings.RegistryFile = value
		case "pause-image":
			settings.PauseImage = value
		case "disable-default-registry-endpoint":
			flag, err := strconv.ParseBool(value)
			if err != nil {
				return Settings{}, errors.New("disable-default-registry-endpoint requires true or false")
			}
			settings.DisableDefaultEndpoint = flag
		default:
			return Settings{}, fmt.Errorf("unknown containerd child flag %q", name)
		}
	}
	if err := settings.applyDefaults(); err != nil {
		return Settings{}, err
	}
	if err := settings.validate(); err != nil {
		return Settings{}, err
	}
	return settings, nil
}

func childFlag(args []string, i int) (string, string, int, error) {
	arg := args[i]
	if arg == "--" || !strings.HasPrefix(arg, "--") {
		return "", "", i, fmt.Errorf("unknown containerd child argument %q", arg)
	}
	body := strings.TrimPrefix(arg, "--")
	name, value, ok := strings.Cut(body, "=")
	if !ok {
		if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
			return "", "", i, fmt.Errorf("%s requires a value", name)
		}
		value = args[i+1]
		i++
	}
	if name == "" || value == "" {
		return "", "", i, fmt.Errorf("%s requires a value", name)
	}
	return name, value, i, nil
}

func (s *Settings) applyDefaults() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	bin := filepath.Dir(exe)
	if s.Socket == "" {
		s.Socket = paths.ContainerdSocket
	}
	if s.Root == "" {
		s.Root = paths.ContainerdRoot
	}
	if s.State == "" {
		s.State = paths.ContainerdState
	}
	if s.CrunBinary == "" {
		s.CrunBinary = filepath.Join(bin, "crun")
	}
	if s.ShimBinary == "" {
		s.ShimBinary = filepath.Join(bin, "containerd-shim-runc-v2")
	}
	if s.CNIBinDir == "" {
		s.CNIBinDir = bin
	}
	if s.CNIConfDir == "" {
		s.CNIConfDir = paths.CNIConfDir
	}
	if s.ConfigFile == "" {
		s.ConfigFile = filepath.Join(s.Root, "config.toml")
	}
	if s.Snapshotter == "" {
		s.Snapshotter = config.DefaultSnapshotter
	}
	if s.LogLevel == "" {
		s.LogLevel = "warn"
	}
	if s.PauseImage == "" {
		s.PauseImage = config.DefaultPauseImage
	}
	if s.RegistryDir == "" {
		s.RegistryDir = filepath.Join(s.Root, "certs.d")
	}
	var errAbs error
	s.Socket, errAbs = absPath(s.Socket)
	if errAbs != nil {
		return errAbs
	}
	s.Root, errAbs = absPath(s.Root)
	if errAbs != nil {
		return errAbs
	}
	s.State, errAbs = absPath(s.State)
	if errAbs != nil {
		return errAbs
	}
	s.ConfigFile, errAbs = absPath(s.ConfigFile)
	if errAbs != nil {
		return errAbs
	}
	s.CrunBinary, errAbs = absPath(s.CrunBinary)
	if errAbs != nil {
		return errAbs
	}
	s.ShimBinary, errAbs = absPath(s.ShimBinary)
	if errAbs != nil {
		return errAbs
	}
	s.CNIBinDir, errAbs = absPath(s.CNIBinDir)
	if errAbs != nil {
		return errAbs
	}
	s.CNIConfDir, errAbs = absPath(s.CNIConfDir)
	if errAbs != nil {
		return errAbs
	}
	s.RegistryDir, errAbs = absPath(s.RegistryDir)
	if errAbs != nil {
		return errAbs
	}
	s.RegistryFile, errAbs = absPath(s.RegistryFile)
	return errAbs
}

func absPath(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("absolute path %s: %w", path, err)
	}
	return abs, nil
}

func (s Settings) validate() error {
	if s.Socket == "" {
		return errors.New("containerd socket is required")
	}
	if err := checkSocket(s.Socket); err != nil {
		return err
	}
	if s.Root == "" {
		return errors.New("containerd root is required")
	}
	if s.State == "" {
		return errors.New("containerd state is required")
	}
	if s.ConfigFile == "" {
		return errors.New("containerd config is required")
	}
	if s.CrunBinary == "" {
		return errors.New("crun binary is required")
	}
	if s.ShimBinary == "" {
		return errors.New("containerd shim is required")
	}
	if s.CNIBinDir == "" {
		return errors.New("cni binary directory is required")
	}
	if err := checkCNIBin(s.CNIBinDir); err != nil {
		return err
	}
	if s.CNIConfDir == "" {
		return errors.New("cni config directory is required")
	}
	if s.Snapshotter == "" {
		return errors.New("snapshotter is required")
	}
	return nil
}

// WriteConfig writes the containerd config for these settings.
// crun is the CRI runtime and SystemdCgroup is enabled.
// A missing registries file leaves the hosts directory empty so containerd uses upstream endpoints.
func WriteConfig(s Settings) error {
	if err := s.validate(); err != nil {
		return err
	}
	if s.PauseImage == "" {
		s.PauseImage = config.DefaultPauseImage
	}
	if s.RegistryDir == "" {
		return errors.New("registry config directory is required")
	}
	auths, err := registries.Prepare(s.RegistryDir, s.RegistryFile, s.DisableDefaultEndpoint)
	if err != nil {
		return err
	}
	tree, err := toml.TreeFromMap(configMap(s, auths))
	if err != nil {
		return fmt.Errorf("containerd config: %w", err)
	}
	var buf bytes.Buffer
	if _, err := tree.WriteTo(&buf); err != nil {
		return fmt.Errorf("render containerd config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.ConfigFile), 0o755); err != nil { // #nosec G301 -- containerd root must be traversable
		return fmt.Errorf("create containerd config directory: %w", err)
	}
	// The file can hold registry credentials.
	if err := os.WriteFile(s.ConfigFile, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("write containerd config: %w", err)
	}
	return nil
}

func configMap(s Settings, auths map[string]registries.Auth) map[string]any {
	runtimes := map[string]any{
		RuntimeName: crunRuntime(s),
	}
	appendDiscoveredRuntimes(runtimes, s.ShimBinary)
	return map[string]any{
		"version":          4,
		"root":             s.Root,
		"state":            s.State,
		"disabled_plugins": []string{},
		"required_plugins": []string{},
		"plugins": map[string]any{
			"io.containerd.server.v1.grpc": map[string]any{
				"address": s.Socket,
				"uid":     os.Getuid(),
				"gid":     os.Getgid(),
			},
			"io.containerd.server.v1.ttrpc": map[string]any{
				"address": s.Socket + ".ttrpc",
				"uid":     os.Getuid(),
				"gid":     os.Getgid(),
			},
			"io.containerd.nri.v1.nri": map[string]any{
				"disable": true,
			},
			"io.containerd.cri.v1.images": imagePlugin(s, auths),
			"io.containerd.cri.v1.runtime": map[string]any{
				"enable_selinux": false,
				"containerd": map[string]any{
					"default_runtime_name": RuntimeName,
					"runtimes":             runtimes,
				},
				"cni": map[string]any{
					"bin_dirs": []string{s.CNIBinDir},
					"conf_dir": s.CNIConfDir,
				},
			},
			"io.containerd.grpc.v1.cri": map[string]any{
				"disable_tcp_service":   true,
				"stream_server_address": "127.0.0.1",
				"stream_server_port":    "0",
			},
			"io.containerd.snapshotter.v1.overlayfs": map[string]any{
				"slow_chown": false,
			},
		},
	}
}

func imagePlugin(s Settings, auths map[string]registries.Auth) map[string]any {
	registry := map[string]any{
		"config_path": s.RegistryDir,
	}
	if len(auths) > 0 {
		configs := map[string]any{}
		for host, auth := range auths {
			if host == "" {
				continue
			}
			entry := map[string]any{}
			if auth.Username != "" {
				entry["username"] = auth.Username
			}
			if auth.Password != "" {
				entry["password"] = auth.Password
			}
			if auth.Token != "" {
				entry["auth"] = auth.Token
			}
			if len(entry) == 0 {
				continue
			}
			configs[host] = map[string]any{"auth": entry}
		}
		if len(configs) > 0 {
			registry["configs"] = configs
		}
	}
	return map[string]any{
		"snapshotter":                  s.Snapshotter,
		"disable_snapshot_annotations": true,
		"pinned_images": map[string]any{
			"sandbox": s.PauseImage,
		},
		"registry": registry,
	}
}

func crunRuntime(s Settings) map[string]any {
	return map[string]any{
		"runtime_type": "io.containerd.runc.v2",
		"runtime_path": s.ShimBinary,
		"options": map[string]any{
			"BinaryName":    s.CrunBinary,
			"SystemdCgroup": true,
		},
	}
}

func appendDiscoveredRuntimes(runtimes map[string]any, shimBinary string) {
	for _, entry := range BuildRuntimeCatalog() {
		if entry.Handler == "" || entry.Path == "" || entry.Handler == RuntimeName {
			continue
		}
		if _, exists := runtimes[entry.Handler]; exists {
			continue
		}
		runtimes[entry.Handler] = discoveredRuntime(entry, shimBinary)
	}
}

func discoveredRuntime(entry RuntimeCatalogEntry, shimBinary string) map[string]any {
	if entry.Family == RuntimeFamilyRunc {
		return map[string]any{
			"runtime_type": entry.RuntimeType,
			"runtime_path": shimBinary,
			"options": map[string]any{
				"BinaryName":    entry.Path,
				"SystemdCgroup": true,
			},
		}
	}
	return map[string]any{
		"runtime_type": entry.RuntimeType,
		"runtime_path": entry.Path,
	}
}
