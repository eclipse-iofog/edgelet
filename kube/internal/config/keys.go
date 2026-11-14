package config

import (
	"fmt"
	"math"
	"strings"
)

// rejectedKeys are server options this process does not accept.
var rejectedKeys = map[string]struct{}{
	"token":                      {},
	"token-file":                 {},
	"agent-token":                {},
	"agent-token-file":           {},
	"server":                     {},
	"cluster-init":               {},
	"cluster-reset":              {},
	"cluster-reset-restore-path": {},
	"lb-server-port":             {},
	"etcd-arg":                   {},
	"etcd-expose-metrics":        {},
	"egress-selector-mode":       {},
	"vpn-auth":                   {},
	"vpn-auth-file":              {},
	"embedded-registry":          {},
	"disable-scheduler":          {},
	"disable-kube-proxy":         {},
	"disable-cloud-controller":   {},
	"disable-network-policy":     {},
	"disable-agent":              {},
	"docker":                     {},
	"rootless":                   {},
}

var rejectedPrefixes = []string{
	"flannel-",
	"etcd-snapshot-",
	"etcd-s3-",
}

type valueKind int

const (
	kindString valueKind = iota
	kindBool
	kindInt
	kindList
)

// keyKind is the value shape of each accepted key.
var keyKind = map[string]valueKind{}

type setter func(*Server, any) error

func stringField(key string, field func(*Server) *string) setter {
	keyKind[key] = kindString
	return func(srv *Server, value any) error {
		text, err := asString(key, value)
		if err != nil {
			return err
		}
		*field(srv) = text
		return nil
	}
}

func boolField(key string, field func(*Server) *bool) setter {
	keyKind[key] = kindBool
	return func(srv *Server, value any) error {
		flag, err := asBool(key, value)
		if err != nil {
			return err
		}
		*field(srv) = flag
		return nil
	}
}

func intField(key string, field func(*Server) *int) setter {
	keyKind[key] = kindInt
	return func(srv *Server, value any) error {
		number, err := asInt(key, value)
		if err != nil {
			return err
		}
		*field(srv) = number
		return nil
	}
}

func listField(key string, field func(*Server) *[]string) setter {
	listFields[key] = field
	keyKind[key] = kindList
	return func(srv *Server, value any) error {
		items, err := asList(key, value)
		if err != nil {
			return err
		}
		if err := checkDisable(key, items); err != nil {
			return err
		}
		*field(srv) = items
		return nil
	}
}

// listFields are the keys a "+" suffix appends to.
var listFields = map[string]func(*Server) *[]string{}

// disableNames are the addon names the disable key accepts.
var disableNames = map[string]struct{}{
	"coredns":         {},
	"servicelb":       {},
	"traefik":         {},
	"local-storage":   {},
	"metrics-server":  {},
	"cert-manager":    {},
	"gateway-api-crd": {},
	"runtimes":        {},
}

var setters = map[string]setter{
	"config":                            stringField("config", func(srv *Server) *string { return &srv.Config }),
	"data-dir":                          stringField("data-dir", func(srv *Server) *string { return &srv.DataDir }),
	"write-kubeconfig":                  stringField("write-kubeconfig", func(srv *Server) *string { return &srv.WriteKubeconfig }),
	"write-kubeconfig-mode":             stringField("write-kubeconfig-mode", func(srv *Server) *string { return &srv.WriteKubeconfigMode }),
	"write-kubeconfig-group":            stringField("write-kubeconfig-group", func(srv *Server) *string { return &srv.WriteKubeconfigGroup }),
	"bind-address":                      stringField("bind-address", func(srv *Server) *string { return &srv.BindAddress }),
	"https-listen-port":                 intField("https-listen-port", func(srv *Server) *int { return &srv.HTTPSListenPort }),
	"advertise-address":                 stringField("advertise-address", func(srv *Server) *string { return &srv.AdvertiseAddress }),
	"advertise-port":                    intField("advertise-port", func(srv *Server) *int { return &srv.AdvertisePort }),
	"tls-san":                           listField("tls-san", func(srv *Server) *[]string { return &srv.TLSSan }),
	"tls-san-security":                  boolField("tls-san-security", func(srv *Server) *bool { return &srv.TLSSanSecurity }),
	"node-name":                         stringField("node-name", func(srv *Server) *string { return &srv.NodeName }),
	"node-ip":                           stringField("node-ip", func(srv *Server) *string { return &srv.NodeIP }),
	"node-external-ip":                  stringField("node-external-ip", func(srv *Server) *string { return &srv.NodeExternalIP }),
	"node-label":                        listField("node-label", func(srv *Server) *[]string { return &srv.NodeLabel }),
	"node-taint":                        listField("node-taint", func(srv *Server) *[]string { return &srv.NodeTaint }),
	"resolv-conf":                       stringField("resolv-conf", func(srv *Server) *string { return &srv.ResolvConf }),
	"cluster-cidr":                      stringField("cluster-cidr", func(srv *Server) *string { return &srv.ClusterCIDR }),
	"service-cidr":                      stringField("service-cidr", func(srv *Server) *string { return &srv.ServiceCIDR }),
	"cluster-dns":                       stringField("cluster-dns", func(srv *Server) *string { return &srv.ClusterDNS }),
	"cluster-domain":                    stringField("cluster-domain", func(srv *Server) *string { return &srv.ClusterDomain }),
	"service-node-port-range":           stringField("service-node-port-range", func(srv *Server) *string { return &srv.ServiceNodePortRange }),
	"pause-image":                       stringField("pause-image", func(srv *Server) *string { return &srv.PauseImage }),
	"datastore-endpoint":                stringField("datastore-endpoint", func(srv *Server) *string { return &srv.DatastoreEndpoint }),
	"datastore-cafile":                  stringField("datastore-cafile", func(srv *Server) *string { return &srv.DatastoreCAFile }),
	"datastore-certfile":                stringField("datastore-certfile", func(srv *Server) *string { return &srv.DatastoreCertFile }),
	"datastore-keyfile":                 stringField("datastore-keyfile", func(srv *Server) *string { return &srv.DatastoreKeyFile }),
	"datastore-nats-creds":              stringField("datastore-nats-creds", func(srv *Server) *string { return &srv.DatastoreNATSCreds }),
	"disable":                           listField("disable", func(srv *Server) *[]string { return &srv.Disable }),
	"disable-helm-controller":           boolField("disable-helm-controller", func(srv *Server) *bool { return &srv.DisableHelmController }),
	"default-local-storage-path":        stringField("default-local-storage-path", func(srv *Server) *string { return &srv.DefaultLocalStoragePath }),
	"cluster-issuer-email":              stringField("cluster-issuer-email", func(srv *Server) *string { return &srv.ClusterIssuerEmail }),
	"debug":                             boolField("debug", func(srv *Server) *bool { return &srv.Debug }),
	"v":                                 intField("v", func(srv *Server) *int { return &srv.V }),
	"vmodule":                           stringField("vmodule", func(srv *Server) *string { return &srv.VModule }),
	"log":                               stringField("log", func(srv *Server) *string { return &srv.Log }),
	"alsologtostderr":                   boolField("alsologtostderr", func(srv *Server) *bool { return &srv.AlsoLogToStderr }),
	"kube-apiserver-arg":                listField("kube-apiserver-arg", func(srv *Server) *[]string { return &srv.KubeAPIServerArg }),
	"kube-controller-manager-arg":       listField("kube-controller-manager-arg", func(srv *Server) *[]string { return &srv.KubeControllerManagerArg }),
	"kube-scheduler-arg":                listField("kube-scheduler-arg", func(srv *Server) *[]string { return &srv.KubeSchedulerArg }),
	"kubelet-arg":                       listField("kubelet-arg", func(srv *Server) *[]string { return &srv.KubeletArg }),
	"kube-proxy-arg":                    listField("kube-proxy-arg", func(srv *Server) *[]string { return &srv.KubeProxyArg }),
	"helm-controller-arg":               listField("helm-controller-arg", func(srv *Server) *[]string { return &srv.HelmControllerArg }),
	"snapshotter":                       stringField("snapshotter", func(srv *Server) *string { return &srv.Snapshotter }),
	"prefer-bundled-bin":                boolField("prefer-bundled-bin", func(srv *Server) *bool { return &srv.PreferBundledBin }),
	"selinux":                           boolField("selinux", func(srv *Server) *bool { return &srv.SELinux }),
	"protect-kernel-defaults":           boolField("protect-kernel-defaults", func(srv *Server) *bool { return &srv.ProtectKernelDefaults }),
	"secrets-encryption":                boolField("secrets-encryption", func(srv *Server) *bool { return &srv.SecretsEncryption }),
	"private-registry":                  stringField("private-registry", func(srv *Server) *string { return &srv.PrivateRegistry }),
	"system-default-registry":           stringField("system-default-registry", func(srv *Server) *string { return &srv.SystemDefaultRegistry }),
	"disable-default-registry-endpoint": boolField("disable-default-registry-endpoint", func(srv *Server) *bool { return &srv.DisableDefaultRegistryEndpoint }),
}

func rejected(key string) bool {
	if _, ok := rejectedKeys[key]; ok {
		return true
	}
	for _, prefix := range rejectedPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func apply(srv *Server, path, key string, value any) error {
	name := key
	appendList := false
	if strings.HasSuffix(key, "+") {
		appendList = true
		name = strings.TrimSuffix(key, "+")
	}
	if rejected(name) {
		return fmt.Errorf("%s: config key %q is not accepted", path, key)
	}
	set, ok := setters[name]
	if !ok {
		return fmt.Errorf("%s: unknown config key %q", path, key)
	}
	// Null leaves the key unset so a default, or an earlier file, still applies.
	if value == nil {
		return nil
	}
	if appendList {
		field, ok := listFields[name]
		if !ok {
			return fmt.Errorf("%s: config key %q is not a list", path, key)
		}
		items, err := asList(key, value)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if err := checkDisable(name, items); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		dst := field(srv)
		*dst = append(*dst, items...)
		srv.present[name] = true
		return nil
	}
	if err := set(srv, value); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	srv.present[name] = true
	return nil
}

func checkDisable(key string, items []string) error {
	if key != "disable" {
		return nil
	}
	for _, name := range items {
		if _, ok := disableNames[name]; !ok {
			return fmt.Errorf("unknown disable name %q", name)
		}
	}
	return nil
}

func asString(key string, value any) (string, error) {
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("config key %q must be a string", key)
	}
	return text, nil
}

func asBool(key string, value any) (bool, error) {
	flag, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("config key %q must be true or false", key)
	}
	return flag, nil
}

func asInt(key string, value any) (int, error) {
	var number int64
	switch typed := value.(type) {
	case int:
		number = int64(typed)
	case int64:
		number = typed
	case uint64:
		if typed > uint64(math.MaxInt64) {
			return 0, fmt.Errorf("config key %q must be an integer", key)
		}
		number = int64(typed)
	default:
		return 0, fmt.Errorf("config key %q must be an integer", key)
	}
	if int64(int(number)) != number {
		return 0, fmt.Errorf("config key %q must be an integer", key)
	}
	return int(number), nil
}

func asList(key string, value any) ([]string, error) {
	switch typed := value.(type) {
	case string:
		return []string{typed}, nil
	case []any:
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("config key %q must be a list of strings", key)
			}
			items = append(items, text)
		}
		return items, nil
	default:
		return nil, fmt.Errorf("config key %q must be a list of strings", key)
	}
}
