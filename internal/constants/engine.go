package constants

// Container engine type names used in config.yaml containerEngine field.
const (
	EngineDocker  = "docker"
	EnginePodman  = "podman"
	EngineEdgelet = "edgelet"
)

// Edgelet embedded containerd — split across three path roots following Linux FHS
//
// /var/lib/edgelet/            — user data (diskDirectory config field)
//
//	diskLimit applies only here
//
// /var/lib/edgelet-containerd/ — containerd images, snapshots, layers
//
//	intentionally outside diskDirectory so image storage
//	does not count against the user's disk limit
//
// /run/edgelet/                — ephemeral runtime files, cleared on reboot
const (
	// User data directory — matches default diskDirectory config value.
	EdgeletDataDir = "/var/lib/edgelet"

	// RuntimeDrainStageRel is the directory under diskDirectory where a
	// hash-mismatch data-plane drain stages the fat runtime. It lives on the
	// data volume so the file can be executed when /run is mounted noexec.
	RuntimeDrainStageRel = "data/.runtime-drain"

	// Containerd persistent data root — separate from EdgeletDataDir so that
	// image layers and snapshots are not counted by diskLimit.
	EdgeletContainerdLibDir     = "/var/lib/edgelet-containerd"
	EdgeletContainerdRootDir    = "/var/lib/edgelet-containerd/root"
	EdgeletContainerdStateDir   = "/var/lib/edgelet-containerd/state"
	EdgeletContainerdBinDir     = "/var/lib/edgelet-containerd/bin"
	EdgeletCNIPluginsDir        = "/var/lib/edgelet-containerd/cni/plugins"
	EdgeletCNIConfDir           = "/var/lib/edgelet-containerd/cni/conf"
	EdgeletContainerdImagesDir  = "/var/lib/edgelet-containerd/images"
	EdgeletManagedCNIConfigName = "10-edgelet.conflist"
	EdgeletCNIConfigFile        = "/var/lib/edgelet-containerd/cni/conf/10-edgelet.conflist"
	EdgeletContainerdConfigFile = "/var/lib/edgelet-containerd/config.toml"
	// EdgeletWasmShimLogDir is the working directory inherited by wasm shim -info.
	// The shim opens a relative file named "log" and does not create it.
	EdgeletWasmShimLogDir = "/var/lib/edgelet-containerd/shim-log"

	// Ephemeral runtime directory — lives on tmpfs on systemd hosts, cleared on reboot.
	EdgeletRunDir           = "/run/edgelet"
	EdgeletContainerdSocket = "/run/edgelet/containerd.sock"

	// Standard system CNI config directory — symlink target for CNI config.
	DefaultSystemCNIConfDir = "/etc/cni/net.d"
	DefaultCNIConfigName    = EdgeletManagedCNIConfigName

	// Edgelet bridge network for containers managed by the embedded engine.
	// Uses 172.18.0.0/16 to avoid conflict with Docker's default 172.17.0.0/16.
	EdgeletBridgeName  = "edgelet0"
	EdgeletBridgeCIDR  = "172.18.0.0/16"
	EdgeletNetworkName = "edgelet"

	// Containerd namespace used for all edgelet-managed containers.
	EdgeletContainerdNamespace = "k8s.io"

	// Sandbox (pause) image for CRI podsandbox. portainer/pause is lightweight and multi-arch (incl. riscv64).
	EdgeletSandboxImage = "portainer/pause:latest"

	// PodmanDefaultDockerURL is the default socket URL for containerEngine podman.
	PodmanDefaultDockerURL = "unix:///run/podman/podman.sock"
)

// EdgeletEngineSocketURL returns the required containerEngineUrl when using embedded containerd (edgelet engine).
func EdgeletEngineSocketURL() string {
	return "unix://" + EdgeletContainerdSocket
}
