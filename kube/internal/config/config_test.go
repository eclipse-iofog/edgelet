package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet-kube/internal/bundle"
)

func TestMissingFileUsesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	got, err := Load([]string{"--config", path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := Defaults()
	want.Config = path
	assertServer(t, got, want)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("config file created: %v", err)
	}
}

func TestEmptyFileUsesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load([]string{"--config", path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := Defaults()
	want.Config = path
	assertServer(t, got, want)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 0 {
		t.Fatalf("empty file was rewritten: %q", body)
	}
}

func TestCommentOnlyFileUsesDefaults(t *testing.T) {
	got := loadPath(t, filepath.Join(t.TempDir(), "config.yaml"), "# no keys\n")
	want := Defaults()
	want.Config = got.Config
	assertServer(t, got, want)
}

func TestDropInLastValueWins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, "data-dir: /from-main\ndebug: true\nnode-name: main\n")
	drop := path + ".d"
	writeFile(t, filepath.Join(drop, "10-a.yaml"), "data-dir: /from-a\nnode-label:\n  - a=1\n")
	writeFile(t, filepath.Join(drop, "20-b.yaml"), "data-dir: /from-b\ndebug: false\n")
	writeFile(t, filepath.Join(drop, "notes.txt"), "not-a-real-key: true\n")
	writeFile(t, filepath.Join(drop, "skip.yml"), "not-a-real-key: true\n")

	got, err := Load([]string{"--config", path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.DataDir != "/from-b" {
		t.Fatalf("data dir %q", got.DataDir)
	}
	if got.Debug {
		t.Fatal("debug stayed true")
	}
	if got.NodeName != "main" {
		t.Fatalf("node name %q", got.NodeName)
	}
	if !reflect.DeepEqual(got.NodeLabel, []string{"a=1"}) {
		t.Fatalf("labels %#v", got.NodeLabel)
	}
}

func TestDropInWithoutMainFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path+".d/10.yaml", "debug: true\n")
	got, err := Load([]string{"--config", path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Debug {
		t.Fatal("drop-in was ignored")
	}
	if got.DataDir != bundle.DefaultDataDir {
		t.Fatalf("data dir %q", got.DataDir)
	}
}

func TestUnknownKeyNamesItself(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, path, "not-a-real-key: true\n")
	_, err := Load([]string{"--config", path}, nil)
	if err == nil || !strings.Contains(err.Error(), "not-a-real-key") || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("error %v", err)
	}
}

func TestRejectedKeysNameThemselves(t *testing.T) {
	keys := []string{
		"token",
		"token-file",
		"agent-token",
		"agent-token-file",
		"server",
		"cluster-init",
		"cluster-reset",
		"cluster-reset-restore-path",
		"lb-server-port",
		"etcd-arg",
		"etcd-expose-metrics",
		"egress-selector-mode",
		"vpn-auth",
		"vpn-auth-file",
		"embedded-registry",
		"disable-scheduler",
		"disable-kube-proxy",
		"disable-cloud-controller",
		"disable-network-policy",
		"disable-agent",
		"docker",
		"rootless",
		"flannel-backend",
		"flannel-iface",
		"etcd-snapshot-dir",
		"etcd-s3-bucket",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			writeFile(t, path, key+": true\n")
			_, err := Load([]string{"--config", path}, nil)
			if err == nil || !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "not accepted") {
				t.Fatalf("error %v", err)
			}
		})
	}
}

func TestBareFlannelIsUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, path, "flannel: true\n")
	_, err := Load([]string{"--config", path}, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown") || !strings.Contains(err.Error(), "flannel") {
		t.Fatalf("error %v", err)
	}
}

func TestConfigFlagMovesDropInDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.yaml")
	writeFile(t, path, "node-name: file\n")
	writeFile(t, path+".d/10.yaml", "node-name: dropin\n")
	writeFile(t, filepath.Join(dir, "config.yaml.d", "10.yaml"), "not-a-real-key: true\n")

	envPath := filepath.Join(t.TempDir(), "from-env.yaml")
	writeFile(t, envPath, "node-name: env\n")

	got, err := Load([]string{"--config", path}, func(key string) (string, bool) {
		if key == EnvConfigFile {
			return envPath, true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.NodeName != "dropin" {
		t.Fatalf("node name %q", got.NodeName)
	}
}

func TestEnvSelectsConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "from-env.yaml")
	writeFile(t, path, "data-dir: /from-env\n")
	got, err := Load(nil, func(key string) (string, bool) {
		if key == EnvConfigFile {
			return path, true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.DataDir != "/from-env" {
		t.Fatalf("data dir %q", got.DataDir)
	}
}

func TestConfigFlagRequiresPath(t *testing.T) {
	_, err := Load([]string{"--config"}, nil)
	if err == nil || !strings.Contains(err.Error(), "config") {
		t.Fatalf("error %v", err)
	}
	_, err = Load([]string{"--config="}, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestConfigPathFlagOverridesEnv(t *testing.T) {
	path, err := configPath([]string{"--config", "/from-flag", "--config=/from-equals"}, func(string) (string, bool) {
		return "/from-env", true
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/from-equals" {
		t.Fatalf("path %q", path)
	}
}

func TestAdvertisePortFollowsListenPort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, path, "https-listen-port: 7443\n")
	got, err := Load([]string{"--config", path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.HTTPSListenPort != 7443 || got.AdvertisePort != 7443 {
		t.Fatalf("listen %d advertise %d", got.HTTPSListenPort, got.AdvertisePort)
	}
}

func TestExplicitFalseAndEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, path, "prefer-bundled-bin: false\ntls-san-security: false\ndata-dir: \"\"\n")
	got, err := Load([]string{"--config", path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.PreferBundledBin || got.TLSSanSecurity {
		t.Fatalf("prefer %v san %v", got.PreferBundledBin, got.TLSSanSecurity)
	}
	if got.DataDir != "" {
		t.Fatalf("data dir %q", got.DataDir)
	}
}

func TestStringListValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, path, "tls-san: site.example.com\n")
	got, err := Load([]string{"--config", path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.TLSSan, []string{"site.example.com"}) {
		t.Fatalf("san %#v", got.TLSSan)
	}
}

func TestWrongTypesNameTheKey(t *testing.T) {
	cases := map[string]string{
		"debug":             "debug: \"true\"\n",
		"https-listen-port": "https-listen-port: \"6443\"\n",
		"tls-san":           "tls-san:\n  - 1\n",
	}
	for key, body := range cases {
		t.Run(key, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			writeFile(t, path, body)
			_, err := Load([]string{"--config", path}, nil)
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("error %v", err)
			}
		})
	}
}

func TestRoundTrip(t *testing.T) {
	const body = `
config: /cfg
data-dir: /data
write-kubeconfig: /kubeconfig
write-kubeconfig-mode: "0644"
write-kubeconfig-group: kube
bind-address: 127.0.0.1
https-listen-port: 7443
advertise-address: 10.1.0.8
advertise-port: 7444
tls-san:
  - a.example
  - b.example
tls-san-security: false
node-name: node-a
node-ip: 10.1.0.9
node-external-ip: 203.0.113.5
node-label:
  - disk=ssd
node-taint:
  - "dedicated=lab:NoSchedule"
resolv-conf: /etc/resolv.conf
cluster-cidr: 10.20.0.0/16
service-cidr: 10.21.0.0/16
cluster-dns: 10.21.0.10
cluster-domain: example.local
service-node-port-range: 30000-30010
pause-image: "pause:test"
datastore-endpoint: "nats://nats.example:4222"
datastore-cafile: /ca.pem
datastore-certfile: /cert.pem
datastore-keyfile: /key.pem
datastore-nats-creds: /nats.creds
disable:
  - traefik
disable-helm-controller: true
default-local-storage-path: /storage
cluster-issuer-email: "ops@example.com"
debug: true
v: 4
vmodule: apiserver=3
log: /var/log/edgelet-kube.log
alsologtostderr: true
kube-apiserver-arg:
  - audit-log-path=/tmp/audit.log
kube-controller-manager-arg:
  - node-cidr-mask-size=24
kube-scheduler-arg:
  - v=2
kubelet-arg:
  - config=/etc/kubelet.yaml
kube-proxy-arg:
  - proxy-mode=iptables
helm-controller-arg:
  - debug=true
snapshotter: native
prefer-bundled-bin: false
selinux: true
protect-kernel-defaults: true
secrets-encryption: true
private-registry: /etc/registries.yaml
system-default-registry: registry.example
disable-default-registry-endpoint: true
`
	for key := range setters {
		if !yamlHasKey(body, key) {
			t.Errorf("sample missing %s", key)
		}
	}
	for key := range rejectedKeys {
		if _, ok := setters[key]; ok {
			t.Errorf("both accepted and rejected: %s", key)
		}
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, path, body)
	// The file selector is not the config key, so the file can still set that key.
	got, err := Load(nil, func(key string) (string, bool) {
		if key == EnvConfigFile {
			return path, true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	want := Server{
		Config:                         "/cfg",
		DataDir:                        "/data",
		WriteKubeconfig:                "/kubeconfig",
		WriteKubeconfigMode:            "0644",
		WriteKubeconfigGroup:           "kube",
		BindAddress:                    "127.0.0.1",
		HTTPSListenPort:                7443,
		AdvertiseAddress:               "10.1.0.8",
		AdvertisePort:                  7444,
		TLSSan:                         []string{"a.example", "b.example"},
		TLSSanSecurity:                 false,
		NodeName:                       "node-a",
		NodeIP:                         "10.1.0.9",
		NodeExternalIP:                 "203.0.113.5",
		NodeLabel:                      []string{"disk=ssd"},
		NodeTaint:                      []string{"dedicated=lab:NoSchedule"},
		ResolvConf:                     "/etc/resolv.conf",
		ClusterCIDR:                    "10.20.0.0/16",
		NodePodCIDR:                    "10.20.0.0/24",
		ServiceCIDR:                    "10.21.0.0/16",
		ClusterDNS:                     "10.21.0.10",
		ClusterDomain:                  "example.local",
		ServiceNodePortRange:           "30000-30010",
		PauseImage:                     "pause:test",
		DatastoreEndpoint:              "nats://nats.example:4222",
		DatastoreCAFile:                "/ca.pem",
		DatastoreCertFile:              "/cert.pem",
		DatastoreKeyFile:               "/key.pem",
		DatastoreNATSCreds:             "/nats.creds",
		Disable:                        []string{"traefik"},
		DisableHelmController:          true,
		DefaultLocalStoragePath:        "/storage",
		ClusterIssuerEmail:             "ops@example.com",
		Debug:                          true,
		V:                              4,
		VModule:                        "apiserver=3",
		Log:                            "/var/log/edgelet-kube.log",
		AlsoLogToStderr:                true,
		KubeAPIServerArg:               []string{"audit-log-path=/tmp/audit.log"},
		KubeControllerManagerArg:       []string{"node-cidr-mask-size=24"},
		KubeSchedulerArg:               []string{"v=2"},
		KubeletArg:                     []string{"config=/etc/kubelet.yaml"},
		KubeProxyArg:                   []string{"proxy-mode=iptables"},
		HelmControllerArg:              []string{"debug=true"},
		Snapshotter:                    "native",
		PreferBundledBin:               false,
		SELinux:                        true,
		ProtectKernelDefaults:          true,
		SecretsEncryption:              true,
		PrivateRegistry:                "/etc/registries.yaml",
		SystemDefaultRegistry:          "registry.example",
		DisableDefaultRegistryEndpoint: true,
	}
	assertServer(t, got, want)
}

func TestDefaultPauseImageMatchesAirgapList(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "scripts", "airgap", "image-list.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var pause string
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "/pause:") {
			pause = line
			break
		}
	}
	name, _, _ := strings.Cut(pause, "@")
	if name != DefaultPauseImage {
		t.Fatalf("pause image %q, list %q", DefaultPauseImage, name)
	}
}

func TestSystemDefaultRegistryPrefixesPause(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, path, "system-default-registry: registry.example\n")
	got, err := Load([]string{"--config", path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.PauseImage != "registry.example/"+DefaultPauseImage {
		t.Fatalf("pause %s", got.PauseImage)
	}
	writeFile(t, path, "system-default-registry: \"\"\npause-image: docker.io/portainer/pause:latest\n")
	got, err = Load([]string{"--config", path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.PauseImage != DefaultPauseImage {
		t.Fatalf("empty registry pause %s", got.PauseImage)
	}
	writeFile(t, path, "system-default-registry: registry.example\npause-image: docker.io/portainer/pause:latest\n")
	got, err = Load([]string{"--config", path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.PauseImage != "registry.example/"+DefaultPauseImage {
		t.Fatalf("listed pause %s", got.PauseImage)
	}
}

func TestKubeletPauseArg(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	got := loadPath(t, path, "# defaults\n")
	pause := DefaultPauseImage
	if got.KubeletPauseArg() != "--pod-infra-container-image="+pause {
		t.Fatalf("pause flag %s", got.KubeletPauseArg())
	}
	if !strings.HasPrefix(airgapPause(t), pause+"@sha256:") {
		t.Fatalf("pause %s is not the pin-list name", pause)
	}

	got = loadPath(t, path, "system-default-registry: registry.example\n")
	flag := got.KubeletPauseArg()
	if flag != "--pod-infra-container-image=registry.example/"+pause {
		t.Fatalf("prefixed pause flag %s", flag)
	}
	if !strings.Contains(flag, "registry.example/") {
		t.Fatalf("prefix missing from %s", flag)
	}

	got = loadPath(t, path, "system-default-registry: \"\"\n")
	if got.KubeletPauseArg() != "--pod-infra-container-image="+pause {
		t.Fatalf("empty registry flag %s", got.KubeletPauseArg())
	}
}

func airgapPause(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "scripts", "airgap", "image-list.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "/pause:") {
			return line
		}
	}
	t.Fatal("pin list has no pause image")
	return ""
}

func TestDefaults(t *testing.T) {
	got := Defaults()
	if got.DataDir != bundle.DefaultDataDir {
		t.Fatalf("data dir %q", got.DataDir)
	}
	if got.WriteKubeconfig != DefaultKubeconfig || got.WriteKubeconfigMode != DefaultKubeconfigMode {
		t.Fatalf("kubeconfig %q mode %q", got.WriteKubeconfig, got.WriteKubeconfigMode)
	}
	if got.BindAddress != DefaultBindAddress || got.HTTPSListenPort != DefaultHTTPSListenPort || got.AdvertisePort != DefaultHTTPSListenPort {
		t.Fatalf("bind %q port %d advertise %d", got.BindAddress, got.HTTPSListenPort, got.AdvertisePort)
	}
	if !got.TLSSanSecurity || !got.PreferBundledBin {
		t.Fatal("expected security and bundled binaries on")
	}
	if got.ClusterCIDR != DefaultClusterCIDR || got.NodePodCIDR != DefaultNodePodCIDR {
		t.Fatalf("pod %s node %s", got.ClusterCIDR, got.NodePodCIDR)
	}
	if got.ServiceCIDR != DefaultServiceCIDR || got.ClusterDNS != DefaultClusterDNS {
		t.Fatalf("service %s dns %s", got.ServiceCIDR, got.ClusterDNS)
	}
	if got.ClusterDomain != DefaultClusterDomain || got.ServiceNodePortRange != DefaultServiceNodePortRange {
		t.Fatalf("domain %s ports %s", got.ClusterDomain, got.ServiceNodePortRange)
	}
	if got.PauseImage != DefaultPauseImage || got.Snapshotter != DefaultSnapshotter {
		t.Fatalf("pause %s snapshotter %s", got.PauseImage, got.Snapshotter)
	}
	if got.PrivateRegistry != DefaultPrivateRegistry || got.DefaultLocalStoragePath != DefaultLocalStoragePath {
		t.Fatalf("registry %s storage %s", got.PrivateRegistry, got.DefaultLocalStoragePath)
	}
	if got.Config != DefaultConfigFile {
		t.Fatalf("config %q", got.Config)
	}
}

func TestDefaultNetwork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	got, err := Load([]string{"--config", path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.ClusterCIDR != "10.42.0.0/16" || got.NodePodCIDR != "10.42.0.0/24" {
		t.Fatalf("pod %s node %s", got.ClusterCIDR, got.NodePodCIDR)
	}
	if got.ServiceCIDR != "10.43.0.0/16" || got.ClusterDNS != "10.43.0.10" {
		t.Fatalf("service %s dns %s", got.ServiceCIDR, got.ClusterDNS)
	}
	if got.ClusterDomain != "cluster.local" || got.ServiceNodePortRange != "30000-32767" {
		t.Fatalf("domain %s ports %s", got.ClusterDomain, got.ServiceNodePortRange)
	}
	dns, err := tenthAddress(got.ServiceCIDR)
	if err != nil {
		t.Fatal(err)
	}
	if dns != got.ClusterDNS {
		t.Fatalf("derived dns %s stored %s", dns, got.ClusterDNS)
	}
	api, err := FirstServiceAddress(got.ServiceCIDR)
	if err != nil {
		t.Fatal(err)
	}
	if api != "10.43.0.1" {
		t.Fatalf("service address %s", api)
	}
	pod, err := nodePodCIDR(got.ClusterCIDR)
	if err != nil {
		t.Fatal(err)
	}
	if pod != got.NodePodCIDR {
		t.Fatalf("derived pod %s stored %s", pod, got.NodePodCIDR)
	}
}

func TestServiceCIDRDerivesClusterDNS(t *testing.T) {
	cases := map[string]string{
		"service-cidr: 192.168.50.0/24\n": "192.168.50.10",
		"service-cidr: 10.1.2.128/25\n":   "10.1.2.138",
	}
	for body, want := range cases {
		t.Run(want, func(t *testing.T) {
			got := loadPath(t, filepath.Join(t.TempDir(), "config.yaml"), body)
			if got.ClusterDNS != want {
				t.Fatalf("dns %s", got.ClusterDNS)
			}
		})
	}
}

func TestClusterDNSOutsideServiceCIDR(t *testing.T) {
	cases := map[string][2]string{
		"service-cidr: 10.50.0.0/16\ncluster-dns: 10.60.0.10\n": {"10.60.0.10", "10.50.0.0/16"},
		"cluster-dns: 10.42.0.10\n":                             {"10.42.0.10", "10.43.0.0/16"},
	}
	for body, names := range cases {
		t.Run(names[0], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			writeFile(t, path, body)
			_, err := Load([]string{"--config", path}, nil)
			if err == nil || !strings.Contains(err.Error(), names[0]) || !strings.Contains(err.Error(), names[1]) {
				t.Fatalf("error %v", err)
			}
		})
	}
}

func TestNodePodCIDR(t *testing.T) {
	got := loadPath(t, filepath.Join(t.TempDir(), "config.yaml"), "cluster-cidr: 10.50.9.0/22\n")
	if got.ClusterCIDR != "10.50.9.0/22" || got.NodePodCIDR != "10.50.8.0/24" {
		t.Fatalf("cluster %s node %s", got.ClusterCIDR, got.NodePodCIDR)
	}
}

func TestBadClusterCIDR(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, path, "cluster-cidr: bananas\n")
	_, err := Load([]string{"--config", path}, nil)
	if err == nil || !strings.Contains(err.Error(), "bananas") || !strings.Contains(err.Error(), "cluster-cidr") {
		t.Fatalf("error %v", err)
	}
}

func TestAppendLists(t *testing.T) {
	cases := []struct {
		base  string
		extra string
		read  func(Server) []string
	}{
		{base: "tls-san:\n  - a.example\n", extra: "tls-san+: b.example\n", read: func(s Server) []string { return s.TLSSan }},
		{base: "kubelet-arg:\n  - v=2\n", extra: "kubelet-arg+: v=3\n", read: func(s Server) []string { return s.KubeletArg }},
		{base: "disable:\n  - traefik\n", extra: "disable+: coredns\n", read: func(s Server) []string { return s.Disable }},
		{base: "node-label:\n  - a=1\n", extra: "node-label+:\n  - b=2\n  - c=3\n", read: func(s Server) []string { return s.NodeLabel }},
	}
	wants := [][]string{
		{"a.example", "b.example"},
		{"v=2", "v=3"},
		{"traefik", "coredns"},
		{"a=1", "b=2", "c=3"},
	}
	for i, tc := range cases {
		t.Run(tc.extra, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			writeFile(t, path, tc.base)
			writeFile(t, path+".d/10.yaml", tc.extra)
			got, err := Load([]string{"--config", path}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(tc.read(got), wants[i]) {
				t.Fatalf("list %#v", tc.read(got))
			}
		})
	}
}

func TestListReplacesWithoutPlus(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, "tls-san:\n  - a.example\n")
	writeFile(t, path+".d/10.yaml", "tls-san:\n  - b.example\n")
	got, err := Load([]string{"--config", path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.TLSSan, []string{"b.example"}) {
		t.Fatalf("san %#v", got.TLSSan)
	}
}

func TestAppendScalarKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, path, "data-dir+: /var/extra\n")
	_, err := Load([]string{"--config", path}, nil)
	if err == nil || !strings.Contains(err.Error(), "data-dir+") {
		t.Fatalf("error %v", err)
	}
}

func TestPlusOnRejectedKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, path, "etcd-arg+: foo\n")
	_, err := Load([]string{"--config", path}, nil)
	if err == nil || !strings.Contains(err.Error(), "etcd-arg+") || !strings.Contains(err.Error(), "not accepted") {
		t.Fatalf("error %v", err)
	}
}

func TestDisableUnknownName(t *testing.T) {
	bodies := []string{
		"disable: [flannel]\n",
		"disable+: flannel\n",
	}
	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			writeFile(t, path, body)
			_, err := Load([]string{"--config", path}, nil)
			if err == nil || !strings.Contains(err.Error(), "flannel") || !strings.Contains(err.Error(), "unknown") {
				t.Fatalf("error %v", err)
			}
		})
	}
}

func TestDisableNames(t *testing.T) {
	got := loadPath(t, filepath.Join(t.TempDir(), "config.yaml"),
		"disable: [coredns, servicelb, traefik, gateway-api-crd, local-storage, metrics-server, cert-manager, runtimes]\n")
	want := []string{"coredns", "servicelb", "traefik", "gateway-api-crd", "local-storage", "metrics-server", "cert-manager", "runtimes"}
	if !reflect.DeepEqual(got.Disable, want) {
		t.Fatalf("disable %#v", got.Disable)
	}
}

func TestRepeatableKeys(t *testing.T) {
	want := []string{
		"tls-san",
		"node-label",
		"node-taint",
		"disable",
		"kube-apiserver-arg",
		"kube-controller-manager-arg",
		"kube-scheduler-arg",
		"kubelet-arg",
		"kube-proxy-arg",
		"helm-controller-arg",
	}
	if len(listFields) != len(want) {
		t.Fatalf("list keys %#v", listKeys(listFields))
	}
	for _, key := range want {
		if _, ok := listFields[key]; !ok {
			t.Errorf("missing list %s", key)
		}
	}
}

func TestEnvNames(t *testing.T) {
	cases := map[string]string{
		"datastore-endpoint": "EDGELET_KUBE_DATASTORE_ENDPOINT",
		"data-dir":           bundle.DataDirEnv,
		"https-listen-port":  "EDGELET_KUBE_HTTPS_LISTEN_PORT",
		"v":                  "EDGELET_KUBE_V",
		"kube-apiserver-arg": "EDGELET_KUBE_KUBE_APISERVER_ARG",
	}
	for key, want := range cases {
		if got := envName(key); got != want {
			t.Errorf("%s -> %s, want %s", key, got, want)
		}
	}
}

func TestPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, `
node-name: from-file
data-dir: /from-file
cluster-domain: file.local
tls-san:
  - file.example
node-label:
  - file=1
cluster-cidr: 10.20.0.0/16
`)
	writeFile(t, path+".d/10-a.yaml", `
node-name: from-a
data-dir: /from-a
`)
	writeFile(t, path+".d/20-b.yaml", `
data-dir: /from-b
cluster-domain: drop.local
tls-san+: drop.example
node-label+: drop=1
`)

	got, err := Load([]string{
		"--config", path,
		"--node-name", "from-flag",
		"--debug=false",
	}, func(key string) (string, bool) {
		switch key {
		case envName("node-name"):
			return "from-env", true
		case envName("cluster-domain"):
			return "env.local", true
		case envName("debug"):
			return "true", true
		case envName("node-label"):
			return "env=1", true
		case envName("cluster-cidr"):
			return "10.50.0.0/16", true
		default:
			return "", false
		}
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("defaults", func(t *testing.T) {
		if got.BindAddress != DefaultBindAddress || got.HTTPSListenPort != DefaultHTTPSListenPort {
			t.Fatalf("bind %q port %d", got.BindAddress, got.HTTPSListenPort)
		}
		if got.ServiceNodePortRange != DefaultServiceNodePortRange {
			t.Fatalf("ports %s", got.ServiceNodePortRange)
		}
	})
	t.Run("drop-in over file", func(t *testing.T) {
		if got.DataDir != "/from-b" {
			t.Fatalf("data dir %q", got.DataDir)
		}
	})
	t.Run("env over drop-in", func(t *testing.T) {
		if got.ClusterDomain != "env.local" {
			t.Fatalf("domain %s", got.ClusterDomain)
		}
		if got.ClusterCIDR != "10.50.0.0/16" || got.NodePodCIDR != "10.50.0.0/24" {
			t.Fatalf("cluster %s node %s", got.ClusterCIDR, got.NodePodCIDR)
		}
	})
	t.Run("flag over env", func(t *testing.T) {
		if got.NodeName != "from-flag" {
			t.Fatalf("node name %q", got.NodeName)
		}
		if got.Debug {
			t.Fatal("debug stayed true")
		}
	})
	t.Run("append then env overrides", func(t *testing.T) {
		if !reflect.DeepEqual(got.TLSSan, []string{"file.example", "drop.example"}) {
			t.Fatalf("san %#v", got.TLSSan)
		}
		if !reflect.DeepEqual(got.NodeLabel, []string{"env=1"}) {
			t.Fatalf("labels %#v", got.NodeLabel)
		}
	})
}

func TestFlagReplacesAppendedList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, "tls-san:\n  - file.example\n")
	writeFile(t, path+".d/10.yaml", "tls-san+: drop.example\n")
	got, err := Load([]string{
		"--config", path,
		"--tls-san", "flag-a.example",
		"--tls-san", "flag-b.example",
	}, func(key string) (string, bool) {
		if key == envName("tls-san") {
			return "env.example", true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"flag-a.example", "flag-b.example"}
	if !reflect.DeepEqual(got.TLSSan, want) {
		t.Fatalf("san %#v", got.TLSSan)
	}
}

func listKeys(fields map[string]func(*Server) *[]string) []string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	return keys
}

func loadPath(t *testing.T, path, body string) Server {
	t.Helper()
	writeFile(t, path, body)
	got, err := Load([]string{"--config", path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func yamlHasKey(body, key string) bool {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), key+":") {
			return true
		}
	}
	return false
}

func assertServer(t *testing.T, got, want Server) {
	t.Helper()
	got.present = nil
	want.present = nil
	if reflect.DeepEqual(got, want) {
		return
	}
	gv := reflect.ValueOf(got)
	wv := reflect.ValueOf(want)
	typ := gv.Type()
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.PkgPath != "" {
			continue
		}
		gf := gv.Field(i).Interface()
		wf := wv.Field(i).Interface()
		if !reflect.DeepEqual(gf, wf) {
			t.Errorf("%s = %#v, want %#v", field.Name, gf, wf)
		}
	}
}
