package control

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/eclipse-iofog/edgelet-kube/internal/bundle"
	"github.com/eclipse-iofog/edgelet-kube/internal/config"
	"github.com/eclipse-iofog/edgelet-kube/internal/paths"
	"github.com/eclipse-iofog/edgelet-kube/internal/version"
)

func init() {
	version.Pin()
}

func apiServerArgs(srv config.Server, endpoint, advertise, encryptionFile string, paths certPaths) []string {
	issuer := "https://kubernetes.default.svc." + srv.ClusterDomain
	values := map[string]string{
		"etcd-servers":                       endpoint,
		"storage-backend":                    "etcd3",
		"allow-privileged":                   "true",
		"authorization-mode":                 "Node,RBAC",
		"anonymous-auth":                     "false",
		"service-account-signing-key-file":   paths.ServiceAccountKey,
		"service-account-key-file":           paths.ServiceAccountCert,
		"service-account-issuer":             issuer,
		"api-audiences":                      issuer,
		"service-cluster-ip-range":           srv.ServiceCIDR,
		"service-node-port-range":            srv.ServiceNodePortRange,
		"secure-port":                        strconv.Itoa(srv.HTTPSListenPort),
		"bind-address":                       srv.BindAddress,
		"tls-cert-file":                      paths.ServingCert,
		"tls-private-key-file":               paths.ServingKey,
		"client-ca-file":                     paths.CACert,
		"kubelet-certificate-authority":      paths.CACert,
		"requestheader-client-ca-file":       paths.FrontProxyCA,
		"requestheader-allowed-names":        "front-proxy-client",
		"proxy-client-cert-file":             paths.FrontProxyCert,
		"proxy-client-key-file":              paths.FrontProxyKey,
		"requestheader-extra-headers-prefix": "X-Remote-Extra-",
		"requestheader-group-headers":        "X-Remote-Group",
		"requestheader-username-headers":     "X-Remote-User",
		"enable-aggregator-routing":          "true",
		"enable-admission-plugins":           "NodeRestriction",
		"profiling":                          "false",
		"cert-dir":                           paths.Dir,
	}
	if paths.APIServerCert != "" && paths.APIServerKey != "" {
		values["kubelet-client-certificate"] = paths.APIServerCert
		values["kubelet-client-key"] = paths.APIServerKey
	}
	if advertise != "" {
		values["advertise-address"] = advertise
	}
	if encryptionFile != "" {
		values["encryption-provider-config"] = encryptionFile
	}
	if srv.DatastoreCAFile != "" {
		values["etcd-cafile"] = srv.DatastoreCAFile
	}
	if srv.DatastoreCertFile != "" {
		values["etcd-certfile"] = srv.DatastoreCertFile
	}
	if srv.DatastoreKeyFile != "" {
		values["etcd-keyfile"] = srv.DatastoreKeyFile
	}
	if srv.V != 0 {
		values["v"] = strconv.Itoa(srv.V)
	}
	if srv.VModule != "" {
		values["vmodule"] = srv.VModule
	}
	return flagArgs(values, srv.KubeAPIServerArg)
}

func controllerManagerArgs(srv config.Server, leader bool, port int, paths certPaths) ([]string, error) {
	mask, err := prefixSize(srv.NodePodCIDR)
	if err != nil {
		return nil, err
	}
	values := map[string]string{
		"kubeconfig":                       paths.ControllerKubeconfig,
		"authorization-kubeconfig":         paths.ControllerKubeconfig,
		"authentication-kubeconfig":        paths.ControllerKubeconfig,
		"service-account-private-key-file": paths.ServiceAccountKey,
		"allocate-node-cidrs":              "true",
		"cluster-cidr":                     srv.ClusterCIDR,
		"node-cidr-mask-size":              mask,
		"service-cluster-ip-range":         srv.ServiceCIDR,
		"root-ca-file":                     paths.CACert,
		"cluster-signing-cert-file":        paths.CACert,
		"cluster-signing-key-file":         paths.CAKey,
		"use-service-account-credentials":  "true",
		"leader-elect":                     strconv.FormatBool(leader),
		"bind-address":                     "127.0.0.1",
		"secure-port":                      strconv.Itoa(port),
		"tls-cert-file":                    paths.ServingCert,
		"tls-private-key-file":             paths.ServingKey,
		"cert-dir":                         paths.Dir,
		"profiling":                        "false",
		"controllers":                      "*,tokencleaner",
	}
	if srv.V != 0 {
		values["v"] = strconv.Itoa(srv.V)
	}
	if srv.VModule != "" {
		values["vmodule"] = srv.VModule
	}
	return flagArgs(values, srv.KubeControllerManagerArg), nil
}

func schedulerArgs(srv config.Server, leader bool, port int, paths certPaths) []string {
	values := map[string]string{
		"kubeconfig":                paths.SchedulerKubeconfig,
		"authorization-kubeconfig":  paths.SchedulerKubeconfig,
		"authentication-kubeconfig": paths.SchedulerKubeconfig,
		"leader-elect":              strconv.FormatBool(leader),
		"bind-address":              "127.0.0.1",
		"secure-port":               strconv.Itoa(port),
		"tls-cert-file":             paths.ServingCert,
		"tls-private-key-file":      paths.ServingKey,
		"cert-dir":                  paths.Dir,
		"profiling":                 "false",
	}
	if srv.V != 0 {
		values["v"] = strconv.Itoa(srv.V)
	}
	if srv.VModule != "" {
		values["vmodule"] = srv.VModule
	}
	return flagArgs(values, srv.KubeSchedulerArg)
}

func kubeletArgs(srv config.Server, nodeIP string, certs certPaths, cniConfDir, cniBinDir string) ([]string, error) {
	confDir, binDir, err := cniDirs(cniConfDir, cniBinDir)
	if err != nil {
		return nil, err
	}
	name, err := nodeName(srv)
	if err != nil {
		return nil, err
	}
	dataDir := srv.DataDir
	if dataDir == "" {
		dataDir = bundle.DefaultDataDir
	}
	root := filepath.Join(dataDir, "agent", "kubelet")
	values := map[string]string{
		"kubeconfig":                 certs.KubeletKubeconfig,
		"container-runtime-endpoint": runtimeEndpoint(),
		"hostname-override":          name,
		"node-ip":                    nodeIP,
		"cluster-dns":                srv.ClusterDNS,
		"cluster-domain":             srv.ClusterDomain,
		"root-dir":                   root,
		"cert-dir":                   filepath.Join(root, "pki"),
		"read-only-port":             "0",
		"register-node":              "true",
	}
	resolv := srv.ResolvConf
	if resolv == "" {
		resolv = hostResolvConf("")
	}
	if resolv != "" {
		values["resolv-conf"] = resolv
	}
	if certs.KubeletServingCert != "" && certs.KubeletServingKey != "" {
		values["tls-cert-file"] = certs.KubeletServingCert
		values["tls-private-key-file"] = certs.KubeletServingKey
	}
	if certs.CACert != "" {
		values["client-ca-file"] = certs.CACert
	}
	if len(srv.NodeLabel) > 0 {
		values["node-labels"] = strings.Join(srv.NodeLabel, ",")
	}
	if len(srv.NodeTaint) > 0 {
		values["register-with-taints"] = strings.Join(srv.NodeTaint, ",")
	}
	if srv.V != 0 {
		values["v"] = strconv.Itoa(srv.V)
	}
	if srv.VModule != "" {
		values["vmodule"] = srv.VModule
	}
	for _, arg := range (paths.Layout{CNIConfDir: confDir, CNIBinDir: binDir}).KubeletCNIArgs() {
		key, value := splitArg(arg)
		values[key] = value
	}
	pauseKey, pauseValue := splitArg(srv.KubeletPauseArg())
	values[pauseKey] = pauseValue
	// The cgroup driver is systemd, matching crun.
	// protect-kernel-defaults is passed when that setting is enabled.
	extra, err := kubeletExtra(srv.KubeletArg)
	if err != nil {
		return nil, err
	}
	values["cgroup-driver"] = "systemd"
	if srv.ProtectKernelDefaults {
		values["protect-kernel-defaults"] = "true"
	}
	return flagArgs(values, extra), nil
}

// kubeletExtra drops arguments that would select another cgroup driver or
// override protect-kernel-defaults. The server setting decides that flag.
func kubeletExtra(extra []string) ([]string, error) {
	if len(extra) == 0 {
		return nil, nil
	}
	kept := make([]string, 0, len(extra))
	for _, raw := range extra {
		key, value := splitArg(raw)
		switch key {
		case "cgroup-driver":
			if value != "systemd" {
				return nil, errors.New("kubelet cgroup driver is systemd")
			}
		case "protect-kernel-defaults":
		default:
			kept = append(kept, raw)
		}
	}
	return kept, nil
}

func kubeProxyArgs(srv config.Server, nodeIP string, certs certPaths) ([]string, error) {
	name, err := nodeName(srv)
	if err != nil {
		return nil, err
	}
	values := map[string]string{
		"proxy-mode":           "iptables",
		"kubeconfig":           certs.KubeProxyKubeconfig,
		"cluster-cidr":         srv.ClusterCIDR,
		"hostname-override":    name,
		"bind-address":         nodeIP,
		"healthz-bind-address": "127.0.0.1:10256",
		"profiling":            "false",
	}
	if srv.V != 0 {
		values["v"] = strconv.Itoa(srv.V)
	}
	if srv.VModule != "" {
		values["vmodule"] = srv.VModule
	}
	return flagArgs(values, srv.KubeProxyArg), nil
}

func cniDirs(confDir, binDir string) (string, string, error) {
	if confDir == "" {
		confDir = paths.CNIConfDir
	}
	if binDir == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", "", fmt.Errorf("resolve executable: %w", err)
		}
		binDir = filepath.Dir(exe)
	}
	if filepath.Clean(binDir) == "/opt/cni/bin" {
		return "", "", fmt.Errorf("unsupported cni binary directory %s", binDir)
	}
	return confDir, binDir, nil
}

func runtimeEndpoint() string {
	socket := paths.ContainerdSocket
	if strings.HasPrefix(socket, "unix://") {
		return socket
	}
	return "unix://" + socket
}

func flagValue(args []string, name string) string {
	prefix := "--" + name + "="
	for _, arg := range args {
		if value, ok := strings.CutPrefix(arg, prefix); ok {
			return value
		}
	}
	return ""
}

func prefixSize(cidr string) (string, error) {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", fmt.Errorf("node pod network %q is not a network", cidr)
	}
	ones, _ := network.Mask.Size()
	return strconv.Itoa(ones), nil
}

func flagArgs(values map[string]string, extra []string) []string {
	merged := make(map[string]string, len(values)+len(extra))
	for key, value := range values {
		merged[key] = value
	}
	for _, raw := range extra {
		key, value := splitArg(raw)
		if key == "" {
			continue
		}
		merged[key] = value
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	args := make([]string, 0, len(keys))
	for _, key := range keys {
		args = append(args, "--"+key+"="+merged[key])
	}
	return args
}

func splitArg(raw string) (string, string) {
	raw = strings.TrimLeft(raw, "-")
	key, value, ok := strings.Cut(raw, "=")
	if key == "" {
		return "", ""
	}
	if !ok {
		return key, "true"
	}
	return key, value
}

func componentPorts(api, controller, scheduler int) (int, int) {
	if controller == 0 {
		if api == config.DefaultHTTPSListenPort {
			controller = 10257
		} else {
			controller = api + 1
		}
	}
	if scheduler == 0 {
		if api == config.DefaultHTTPSListenPort {
			scheduler = 10259
		} else {
			scheduler = api + 2
		}
	}
	return controller, scheduler
}
