package containerd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet-kube/internal/config"
	"github.com/eclipse-iofog/edgelet-kube/internal/paths"
	"github.com/pelletier/go-toml"
)

func TestMaybeRunChildLeavesServerAlone(t *testing.T) {
	handled, err := MaybeRunChild([]string{"edgelet-kube", "server"})
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("server was handled as the containerd child")
	}
}

func TestWantsClient(t *testing.T) {
	if WantsClient("crictl", []string{"crictl", "--data-dir", "/data"}) {
		t.Fatal("data-dir alone is the recognition path")
	}
	if WantsClient("kubectl", []string{"kubectl", "get", "nodes"}) {
		t.Fatal("kubectl is not a containerd client")
	}
	if !WantsClient("ctr", []string{"ctr", "version"}) {
		t.Fatal("ctr version should run the client")
	}
	if !WantsClient("crictl", []string{"crictl", "--runtime-endpoint", "/tmp/c.sock", "version"}) {
		t.Fatal("crictl version should run the client")
	}
}

func TestClientsUseSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "containerd.sock")
	got, rest, err := clientSocket([]string{"--data-dir", "/data", "--address", sock, "version"})
	if err != nil {
		t.Fatal(err)
	}
	if got != sock {
		t.Fatalf("socket %q", got)
	}
	if len(rest) != 1 || rest[0] != "version" {
		t.Fatalf("args %#v", rest)
	}
	got, _, err = clientSocket([]string{"--runtime-endpoint", "unix://" + sock, "info"})
	if err != nil {
		t.Fatal(err)
	}
	if got != sock {
		t.Fatalf("socket %q", got)
	}
	for _, blocked := range []string{
		"/run/containerd/containerd.sock",
		"/var/run/containerd/containerd.sock",
		"unix:///run/containerd/containerd.sock",
	} {
		if _, _, err := clientSocket([]string{"--address", blocked, "version"}); err == nil {
			t.Fatalf("accepted %s", blocked)
		}
	}
}

func TestDefaultRuntimePaths(t *testing.T) {
	settings, err := ParseChildArgs([]string{"edgelet-kube", ChildArg})
	if err != nil {
		t.Fatal(err)
	}
	if settings.Socket != paths.ContainerdSocket {
		t.Fatalf("socket %s", settings.Socket)
	}
	if settings.Root != paths.ContainerdRoot {
		t.Fatalf("root %s", settings.Root)
	}
	if settings.State != paths.ContainerdState {
		t.Fatalf("state %s", settings.State)
	}
	if settings.CNIConfDir != paths.CNIConfDir {
		t.Fatalf("cni conf %s", settings.CNIConfDir)
	}
	if settings.Snapshotter != "overlayfs" {
		t.Fatalf("snapshotter %s", settings.Snapshotter)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wantBin, err := filepath.Abs(filepath.Dir(exe))
	if err != nil {
		t.Fatal(err)
	}
	if settings.CNIBinDir != wantBin {
		t.Fatalf("cni bin %s", settings.CNIBinDir)
	}
	if settings.CNIBinDir == "/opt/cni/bin" || settings.Socket == "/run/containerd/containerd.sock" {
		t.Fatalf("host path in %+v", settings)
	}
	got, rest, err := clientSocket([]string{"version"})
	if err != nil {
		t.Fatal(err)
	}
	if got != paths.ContainerdSocket {
		t.Fatalf("client socket %s", got)
	}
	if len(rest) != 1 || rest[0] != "version" {
		t.Fatalf("args %#v", rest)
	}
}

func TestWriteConfigUsesCrun(t *testing.T) {
	hideDiscoveredRuntimes(t)
	dir := t.TempDir()
	settings, err := ParseChildArgs([]string{
		"edgelet-kube",
		ChildArg,
		"--address", filepath.Join(dir, "containerd.sock"),
		"--root", filepath.Join(dir, "root"),
		"--state", filepath.Join(dir, "state"),
		"--crun", filepath.Join(dir, "crun"),
		"--shim", filepath.Join(dir, "containerd-shim-runc-v2"),
		"--cni-bin-dir", filepath.Join(dir, "bin"),
		"--cni-conf-dir", filepath.Join(dir, "net.d"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if settings.Snapshotter != config.DefaultSnapshotter {
		t.Fatalf("snapshotter %q", settings.Snapshotter)
	}
	if err := WriteConfig(settings); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(settings.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		`default_runtime_name = "crun"`,
		`runtime_type = "io.containerd.runc.v2"`,
		"SystemdCgroup = true",
		`snapshotter = "overlayfs"`,
		settings.CrunBinary,
		settings.ShimBinary,
		settings.Socket,
		settings.CNIBinDir,
	} {
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

func TestMissingRegistriesFileUsesUpstream(t *testing.T) {
	dir := t.TempDir()
	settings := childSettings(t, dir)
	settings.RegistryFile = filepath.Join(dir, "missing.yaml")
	if err := WriteConfig(settings); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(settings.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), settings.RegistryDir) {
		t.Fatalf("config path missing\n%s", body)
	}
	entries, err := os.ReadDir(settings.RegistryDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("hosts written for a missing file: %d", len(entries))
	}
	if !strings.Contains(string(body), config.DefaultPauseImage) {
		t.Fatalf("pause image missing\n%s", body)
	}
}

func TestRegistriesAndPauseImageReachContainerd(t *testing.T) {
	dir := t.TempDir()
	registriesFile := filepath.Join(dir, "registries.yaml")
	if err := os.WriteFile(registriesFile, []byte(`
mirrors:
  docker.io:
    endpoint:
      - https://mirror.example.com
configs:
  mirror.example.com:
    auth:
      username: user
      password: secret
      auth: dXNlcjpwYXNz
`), 0o644); err != nil {
		t.Fatal(err)
	}
	settings := childSettings(t, dir)
	settings.RegistryFile = registriesFile
	settings.DisableDefaultEndpoint = true
	settings.PauseImage = "registry.example/docker.io/portainer/pause:latest"
	if err := WriteConfig(settings); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(settings.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := toml.LoadBytes(body)
	if err != nil {
		t.Fatal(err)
	}
	images := tree.GetPath([]string{"plugins", "io.containerd.cri.v1.images"})
	plugin, ok := images.(*toml.Tree)
	if !ok {
		t.Fatalf("images plugin %#v", images)
	}
	if got := plugin.GetPath([]string{"registry", "config_path"}); got != settings.RegistryDir {
		t.Fatalf("config_path %v", got)
	}
	if got := plugin.GetPath([]string{"pinned_images", "sandbox"}); got != settings.PauseImage {
		t.Fatalf("sandbox %v", got)
	}
	auth := plugin.GetPath([]string{"registry", "configs", "mirror.example.com", "auth"})
	authTree, ok := auth.(*toml.Tree)
	if !ok {
		t.Fatalf("auth %#v\n%s", auth, body)
	}
	if authTree.Get("username") != "user" || authTree.Get("password") != "secret" || authTree.Get("auth") != "dXNlcjpwYXNz" {
		t.Fatalf("auth %#v", authTree.ToMap())
	}
	mirror := filepath.Join(settings.RegistryDir, "docker.io", "hosts.toml")
	hosts, err := os.ReadFile(mirror)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(hosts), "registry-1.docker.io") {
		t.Fatalf("upstream fallback still present\n%s", hosts)
	}
	if !strings.Contains(string(hosts), "https://mirror.example.com/v2") {
		t.Fatalf("mirror missing\n%s", hosts)
	}
}

func childSettings(t *testing.T, dir string) Settings {
	t.Helper()
	hideDiscoveredRuntimes(t)
	settings, err := ParseChildArgs([]string{
		"edgelet-kube",
		ChildArg,
		"--address", filepath.Join(dir, "containerd.sock"),
		"--root", filepath.Join(dir, "root"),
		"--state", filepath.Join(dir, "state"),
		"--crun", filepath.Join(dir, "crun"),
		"--shim", filepath.Join(dir, "containerd-shim-runc-v2"),
		"--cni-bin-dir", filepath.Join(dir, "bin"),
		"--cni-conf-dir", filepath.Join(dir, "net.d"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return settings
}

func TestParseChildRejectsHostPaths(t *testing.T) {
	dir := t.TempDir()
	_, err := ParseChildArgs([]string{
		"edgelet-kube",
		ChildArg,
		"--address", "/run/containerd/containerd.sock",
		"--root", filepath.Join(dir, "root"),
		"--state", filepath.Join(dir, "state"),
	})
	if err == nil {
		t.Fatal("accepted the host containerd socket")
	}
	_, err = ParseChildArgs([]string{
		"edgelet-kube",
		ChildArg,
		"--address", filepath.Join(dir, "containerd.sock"),
		"--root", filepath.Join(dir, "root"),
		"--state", filepath.Join(dir, "state"),
		"--cni-bin-dir", "/opt/cni/bin",
	})
	if err == nil {
		t.Fatal("accepted the host cni directory")
	}
}

func TestWriteConfigRegistersDiscoveredRuntimes(t *testing.T) {
	prev := lookPathForRuntimeCatalog
	t.Cleanup(func() { lookPathForRuntimeCatalog = prev })
	lookPathForRuntimeCatalog = func(file string) (string, error) {
		switch file {
		case "nvidia-container-runtime":
			return "/usr/bin/nvidia-container-runtime", nil
		case "containerd-shim-spin-v2":
			return "/usr/local/bin/containerd-shim-spin-v2", nil
		default:
			return "", errors.New("not found")
		}
	}

	dir := t.TempDir()
	settings, err := ParseChildArgs([]string{
		"edgelet-kube",
		ChildArg,
		"--address", filepath.Join(dir, "containerd.sock"),
		"--root", filepath.Join(dir, "root"),
		"--state", filepath.Join(dir, "state"),
		"--crun", filepath.Join(dir, "crun"),
		"--shim", filepath.Join(dir, "containerd-shim-runc-v2"),
		"--cni-bin-dir", filepath.Join(dir, "bin"),
		"--cni-conf-dir", filepath.Join(dir, "net.d"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteConfig(settings); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(settings.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := toml.LoadBytes(body)
	if err != nil {
		t.Fatal(err)
	}
	plugins, ok := tree.GetPath([]string{"plugins"}).(*toml.Tree)
	if !ok {
		t.Fatalf("plugins %#v", tree.Get("plugins"))
	}
	runtimePlugin, ok := plugins.GetPath([]string{"io.containerd.cri.v1.runtime"}).(*toml.Tree)
	if !ok {
		t.Fatalf("runtime plugin %#v", plugins.GetPath([]string{"io.containerd.cri.v1.runtime"}))
	}
	containerdTree, ok := runtimePlugin.Get("containerd").(*toml.Tree)
	if !ok {
		t.Fatal("containerd runtime table missing")
	}
	if got := containerdTree.Get("default_runtime_name"); got != RuntimeName {
		t.Fatalf("default runtime %v", got)
	}
	runtimes, ok := containerdTree.Get("runtimes").(*toml.Tree)
	if !ok {
		t.Fatal("runtimes table missing")
	}
	crunOpts := runtimeOptions(t, runtimes, "crun")
	if got := crunOpts.Get("BinaryName"); got != settings.CrunBinary {
		t.Fatalf("crun binary %v", got)
	}
	if got, ok := crunOpts.Get("SystemdCgroup").(bool); !ok || !got {
		t.Fatalf("crun SystemdCgroup %v", crunOpts.Get("SystemdCgroup"))
	}
	nvidia, ok := runtimes.Get("nvidia").(*toml.Tree)
	if !ok {
		t.Fatal("nvidia runtime missing")
	}
	if got := nvidia.Get("runtime_path"); got != settings.ShimBinary {
		t.Fatalf("nvidia shim %v", got)
	}
	nvidiaOpts := runtimeOptions(t, runtimes, "nvidia")
	if got := nvidiaOpts.Get("BinaryName"); got != "/usr/bin/nvidia-container-runtime" {
		t.Fatalf("nvidia binary %v", got)
	}
	if got, ok := nvidiaOpts.Get("SystemdCgroup").(bool); !ok || !got {
		t.Fatalf("nvidia SystemdCgroup %v", nvidiaOpts.Get("SystemdCgroup"))
	}
	spin, ok := runtimes.Get("spin").(*toml.Tree)
	if !ok {
		t.Fatal("spin runtime missing")
	}
	if got := spin.Get("runtime_type"); got != "io.containerd.spin.v2" {
		t.Fatalf("spin runtime type %v", got)
	}
	if got := spin.Get("runtime_path"); got != "/usr/local/bin/containerd-shim-spin-v2" {
		t.Fatalf("spin path %v", got)
	}
	if spin.Get("options") != nil {
		t.Fatal("spin shim should not carry runc options")
	}
	if runtimes.Get("wasmer") != nil {
		t.Fatal("missing wasmer binary was registered")
	}
}

func runtimeOptions(t *testing.T, runtimes *toml.Tree, name string) *toml.Tree {
	t.Helper()
	runtime, ok := runtimes.Get(name).(*toml.Tree)
	if !ok {
		t.Fatalf("runtime %s missing", name)
	}
	options, ok := runtime.Get("options").(*toml.Tree)
	if !ok {
		t.Fatalf("runtime %s options missing", name)
	}
	return options
}

func hideDiscoveredRuntimes(t *testing.T) {
	t.Helper()
	prev := lookPathForRuntimeCatalog
	t.Cleanup(func() { lookPathForRuntimeCatalog = prev })
	lookPathForRuntimeCatalog = func(string) (string, error) {
		return "", errors.New("not found")
	}
}
