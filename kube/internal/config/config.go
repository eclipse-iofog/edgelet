// Package config loads server options from the config file and its drop-ins.
package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/eclipse-iofog/edgelet-kube/internal/bundle"
	"github.com/eclipse-iofog/edgelet-kube/scripts/airgap"
)

const (
	// DefaultConfigFile is the config file when --config and EDGELET_KUBE_CONFIG_FILE are unset.
	DefaultConfigFile = "/etc/edgelet-kube/config.yaml"
	// EnvConfigFile is the environment variable that moves the config file.
	EnvConfigFile = "EDGELET_KUBE_CONFIG_FILE"

	// DefaultKubeconfig is the admin kubeconfig path.
	DefaultKubeconfig = "/etc/edgelet-kube/edgelet-kube.yaml"
	// DefaultKubeconfigMode is the admin kubeconfig file mode.
	DefaultKubeconfigMode = "0600"
	// DefaultBindAddress is the API bind address.
	DefaultBindAddress = "0.0.0.0"
	// DefaultHTTPSListenPort is the API listen port.
	DefaultHTTPSListenPort = 6443

	// DefaultClusterCIDR is the pod network when cluster-cidr is unset.
	DefaultClusterCIDR = "10.42.0.0/16"
	// DefaultServiceCIDR is the service network when service-cidr is unset.
	DefaultServiceCIDR = "10.43.0.0/16"
	// DefaultClusterDNS is the cluster DNS address of the default service network.
	DefaultClusterDNS = "10.43.0.10"
	// DefaultNodePodCIDR is this node's pod range when cluster-cidr is unset.
	DefaultNodePodCIDR = "10.42.0.0/24"
	// DefaultClusterDomain is the cluster domain when cluster-domain is unset.
	DefaultClusterDomain = "cluster.local"
	// DefaultServiceNodePortRange is the NodePort range.
	DefaultServiceNodePortRange = "30000-32767"
	// DefaultPauseImage is the pause image on the airgap list, without the digest.
	// It is the portainer/pause image the Edgelet agent imports.
	DefaultPauseImage = "docker.io/portainer/pause:latest"

	// DefaultSnapshotter is the container snapshot driver.
	DefaultSnapshotter = "overlayfs"
	// DefaultPrivateRegistry is the registries file.
	DefaultPrivateRegistry = "/etc/edgelet-kube/registries.yaml"
	// DefaultLocalStoragePath is the local-path volume directory.
	DefaultLocalStoragePath = bundle.DefaultDataDir + "/storage"
)

// Server is the server configuration loaded from the config file and its drop-ins.
type Server struct {
	// Config is the config file path.
	Config string
	// DataDir is the data directory.
	DataDir string
	// WriteKubeconfig is the admin kubeconfig path.
	WriteKubeconfig string
	// WriteKubeconfigMode is the admin kubeconfig file mode.
	WriteKubeconfigMode string
	// WriteKubeconfigGroup is the admin kubeconfig group.
	WriteKubeconfigGroup string

	// BindAddress is the API bind address.
	BindAddress string
	// HTTPSListenPort is the API listen port.
	HTTPSListenPort int
	// AdvertiseAddress is the address advertised to the cluster.
	AdvertiseAddress string
	// AdvertisePort is the port advertised to the cluster.
	AdvertisePort int
	// TLSSan is the extra names on the serving certificate.
	TLSSan []string
	// TLSSanSecurity keeps extra serving names to the node and the SAN list.
	TLSSanSecurity bool

	// NodeName is the node name.
	NodeName string
	// NodeIP is the node IP.
	NodeIP string
	// NodeExternalIP is the external node IP.
	NodeExternalIP string
	// NodeLabel is the node labels.
	NodeLabel []string
	// NodeTaint is the node taints.
	NodeTaint []string
	// ResolvConf is the resolver file passed to kubelet.
	ResolvConf string

	// ClusterCIDR is the pod network.
	ClusterCIDR string
	// NodePodCIDR is this node's pod range, the first /24 of ClusterCIDR.
	NodePodCIDR string
	// ServiceCIDR is the service network.
	ServiceCIDR string
	// ClusterDNS is the cluster DNS address.
	ClusterDNS string
	// ClusterDomain is the cluster DNS domain.
	ClusterDomain string
	// ServiceNodePortRange is the NodePort range.
	ServiceNodePortRange string
	// PauseImage is the pause image kubelet uses. When system-default-registry
	// is set and this name is on the pin list, the registry is prefixed.
	PauseImage string

	// DatastoreEndpoint is the database endpoint. Empty selects SQLite.
	DatastoreEndpoint string
	// DatastoreCAFile is the database CA file.
	DatastoreCAFile string
	// DatastoreCertFile is the database client certificate.
	DatastoreCertFile string
	// DatastoreKeyFile is the database client key.
	DatastoreKeyFile string
	// DatastoreNATSCreds is the NATS credentials file.
	DatastoreNATSCreds string

	// Disable is the addon names to leave off.
	Disable []string
	// DisableHelmController leaves the Helm controller off.
	DisableHelmController bool
	// DefaultLocalStoragePath is the local-path volume directory.
	DefaultLocalStoragePath string
	// ClusterIssuerEmail is the Let's Encrypt account email.
	ClusterIssuerEmail string

	// Debug turns on debug logging.
	Debug bool
	// V is the log verbosity.
	V int
	// VModule is the file-filtered log verbosity.
	VModule string
	// Log is the log file path.
	Log string
	// AlsoLogToStderr logs to stderr as well as the log file.
	AlsoLogToStderr bool

	// KubeAPIServerArg is the extra kube-apiserver arguments.
	KubeAPIServerArg []string
	// KubeControllerManagerArg is the extra kube-controller-manager arguments.
	KubeControllerManagerArg []string
	// KubeSchedulerArg is the extra kube-scheduler arguments.
	KubeSchedulerArg []string
	// KubeletArg is the extra kubelet arguments.
	KubeletArg []string
	// KubeProxyArg is the extra kube-proxy arguments.
	KubeProxyArg []string
	// HelmControllerArg is the extra Helm controller arguments.
	HelmControllerArg []string

	// Snapshotter is the container snapshot driver.
	Snapshotter string
	// PreferBundledBin prefers the bundled host binaries.
	PreferBundledBin bool
	// SELinux enables SELinux.
	SELinux bool
	// ProtectKernelDefaults leaves kernel sysctls unchanged.
	ProtectKernelDefaults bool
	// SecretsEncryption encrypts secrets at rest.
	SecretsEncryption bool

	// PrivateRegistry is the registries file path.
	PrivateRegistry string
	// SystemDefaultRegistry is the registry prefix for pinned images.
	SystemDefaultRegistry string
	// DisableDefaultRegistryEndpoint skips the upstream registry fallback.
	DisableDefaultRegistryEndpoint bool

	// present records keys the files set, so a later default does not
	// replace an explicit false or empty value.
	present map[string]bool
}

// Defaults returns the built-in server options.
func Defaults() Server {
	var srv Server
	if err := srv.applyDefaults(DefaultConfigFile); err != nil {
		panic(err)
	}
	return srv
}

// Load reads the config file selected by args and lookupEnv, then the drop-ins
// beside that file, then EDGELET_KUBE_* variables, then flags. A flag replaces
// the same key from the environment. lookupEnv may be nil. A missing file and
// an empty file use Defaults. Load does not create a config file.
func Load(args []string, lookupEnv func(string) (string, bool)) (Server, error) {
	path, err := configPath(args, lookupEnv)
	if err != nil {
		return Server{}, err
	}
	srv := Server{present: map[string]bool{}}
	if err := mergeFile(&srv, path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Server{}, err
	}
	drops, err := dropIns(path)
	if err != nil {
		return Server{}, err
	}
	for _, drop := range drops {
		if err := mergeFile(&srv, drop); err != nil {
			return Server{}, err
		}
	}
	if err := applyEnv(&srv, lookupEnv); err != nil {
		return Server{}, err
	}
	if err := applyFlags(&srv, args); err != nil {
		return Server{}, err
	}
	if err := srv.applyDefaults(path); err != nil {
		return Server{}, err
	}
	return srv, nil
}

func configPath(args []string, lookupEnv func(string) (string, bool)) (string, error) {
	path := DefaultConfigFile
	if lookupEnv != nil {
		if value, ok := lookupEnv(EnvConfigFile); ok && value != "" {
			path = value
		}
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			return filepath.Clean(path), nil
		case arg == "--config":
			if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
				return "", errors.New("config requires a path")
			}
			i++
			path = args[i]
		case strings.HasPrefix(arg, "--config="):
			value := strings.TrimPrefix(arg, "--config=")
			if value == "" {
				return "", errors.New("config requires a path")
			}
			path = value
		}
	}
	return filepath.Clean(path), nil
}

func (s *Server) applyDefaults(loadedFrom string) error {
	if !s.present["config"] {
		s.Config = loadedFrom
	}
	if !s.present["data-dir"] {
		s.DataDir = bundle.DefaultDataDir
	}
	if !s.present["write-kubeconfig"] {
		s.WriteKubeconfig = DefaultKubeconfig
	}
	if !s.present["write-kubeconfig-mode"] {
		s.WriteKubeconfigMode = DefaultKubeconfigMode
	}
	if !s.present["bind-address"] {
		s.BindAddress = DefaultBindAddress
	}
	if !s.present["https-listen-port"] {
		s.HTTPSListenPort = DefaultHTTPSListenPort
	}
	if !s.present["advertise-port"] {
		s.AdvertisePort = s.HTTPSListenPort
	}
	if !s.present["tls-san-security"] {
		s.TLSSanSecurity = true
	}
	if err := s.applyNetwork(); err != nil {
		return err
	}
	if !s.present["pause-image"] {
		s.PauseImage = DefaultPauseImage
	}
	s.PauseImage = airgap.Resolve(s.SystemDefaultRegistry, s.PauseImage)
	if !s.present["default-local-storage-path"] {
		s.DefaultLocalStoragePath = DefaultLocalStoragePath
	}
	if !s.present["snapshotter"] {
		s.Snapshotter = DefaultSnapshotter
	}
	if !s.present["prefer-bundled-bin"] {
		s.PreferBundledBin = true
	}
	if !s.present["private-registry"] {
		s.PrivateRegistry = DefaultPrivateRegistry
	}
	return nil
}

// KubeletPauseArg is the kubelet flag for the sandbox image.
// The value is PauseImage. A set system-default-registry is already in that name.
func (s Server) KubeletPauseArg() string {
	return "--pod-infra-container-image=" + s.PauseImage
}
