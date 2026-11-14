package stage

import (
	"embed"
	"encoding/json"
	"fmt"
	"net"
	"runtime"
	"strings"

	"github.com/eclipse-iofog/edgelet-kube/internal/config"
)

//go:embed coredns.yaml local-path.yaml metrics-server.yaml helmchart-crd.yaml
var files embed.FS

type manifest struct {
	name    string
	enabled bool
	body    []byte
}

func addonManifests(srv config.Server) ([]manifest, error) {
	dns := srv.ClusterDNS
	if dns == "" {
		dns = config.DefaultClusterDNS
	}
	if net.ParseIP(dns) == nil {
		return nil, fmt.Errorf("cluster dns %q is not an IP", dns)
	}
	domain := srv.ClusterDomain
	if domain == "" {
		domain = config.DefaultClusterDomain
	}
	if strings.ContainsAny(domain, " \t\r\n\"'{}") {
		return nil, fmt.Errorf("cluster domain %q cannot be used for cluster dns", domain)
	}
	storage := srv.DefaultLocalStoragePath
	if storage == "" {
		storage = config.DefaultLocalStoragePath
	}
	pathJSON, err := json.Marshal(storage)
	if err != nil {
		return nil, fmt.Errorf("local storage path: %w", err)
	}
	corednsImage, err := imageRef(srv.SystemDefaultRegistry, "coredns/coredns")
	if err != nil {
		return nil, err
	}
	localPathImage, err := imageRef(srv.SystemDefaultRegistry, "rancher/local-path-provisioner")
	if err != nil {
		return nil, err
	}
	busyboxImage, err := imageRef(srv.SystemDefaultRegistry, "library/busybox")
	if err != nil {
		return nil, err
	}
	metricsImage, err := imageRef(srv.SystemDefaultRegistry, "metrics-server/metrics-server")
	if err != nil {
		return nil, err
	}
	replacer := strings.NewReplacer(
		"__CLUSTER_DNS__", dns,
		"__CLUSTER_DOMAIN__", domain,
		"__STORAGE_PATH_JSON__", string(pathJSON),
		"__COREDNS_IMAGE__", corednsImage,
		"__LOCAL_PATH_IMAGE__", localPathImage,
		"__BUSYBOX_IMAGE__", busyboxImage,
		"__METRICS_SERVER_IMAGE__", metricsImage,
	)
	out := make([]manifest, 0, len(addonFiles))
	for _, file := range addonFiles {
		body, err := files.ReadFile(file.name)
		if err != nil {
			return nil, fmt.Errorf("read addon %s: %w", file.addon, err)
		}
		rendered := []byte(replacer.Replace(string(body)))
		if strings.Contains(string(rendered), "__") {
			return nil, fmt.Errorf("addon %s has an unreplaced value", file.addon)
		}
		out = append(out, manifest{
			name:    file.addon,
			enabled: !addonOff(srv.Disable, file.addon),
			body:    rendered,
		})
	}
	return out, nil
}

type addonFile struct {
	addon string
	name  string
}

var addonFiles = []addonFile{
	{addon: "coredns", name: "coredns.yaml"},
	{addon: "local-storage", name: "local-path.yaml"},
	{addon: "metrics-server", name: "metrics-server.yaml"},
}

// stageArch selects addons that are not published for one architecture.
// riscv64 does not stage metrics-server or cert-manager.
var stageArch = runtime.GOARCH

func disabled(names []string, name string) bool {
	for _, item := range names {
		if item == name {
			return true
		}
	}
	return false
}

func addonOff(names []string, name string) bool {
	if disabled(names, name) {
		return true
	}
	return stageArch == "riscv64" && (name == "metrics-server" || name == "cert-manager")
}
