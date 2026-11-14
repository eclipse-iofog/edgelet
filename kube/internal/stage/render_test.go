package stage

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet-kube/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestMain(m *testing.M) {
	stageArch = "amd64"
	os.Exit(m.Run())
}

func TestManifestsUseClusterNetworkAndPinnedImages(t *testing.T) {
	srv := config.Defaults()
	srv.ClusterDNS = "10.50.0.25"
	srv.ClusterDomain = "example.test"
	srv.DefaultLocalStoragePath = "/var/lib/edgelet-kube/volumes"
	srv.SystemDefaultRegistry = "registry.example:5000"
	docs, err := addonManifests(srv)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 3 {
		t.Fatalf("addons %d", len(docs))
	}
	body := map[string]string{}
	for _, doc := range docs {
		if !doc.enabled {
			t.Fatalf("%s disabled", doc.name)
		}
		text := string(doc.body)
		if strings.Contains(text, "__") {
			t.Fatalf("%s has an unreplaced value", doc.name)
		}
		for _, absent := range []string{"traefik", "cert-manager", "klipper", "datasance", "taints:"} {
			if strings.Contains(strings.ToLower(text), absent) {
				t.Fatalf("%s contains %s", doc.name, absent)
			}
		}
		body[doc.name] = text
	}
	for _, repo := range []string{"coredns/coredns", "rancher/local-path-provisioner", "library/busybox", "metrics-server/metrics-server"} {
		ref, err := imageRef(srv.SystemDefaultRegistry, repo)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(body["coredns"]+body["local-storage"]+body["metrics-server"], ref) {
			t.Fatalf("missing pinned image %s", ref)
		}
		if !strings.HasPrefix(ref, "registry.example:5000/") {
			t.Fatalf("image %s", ref)
		}
	}
	objs, err := decode([]byte(body["coredns"]))
	if err != nil {
		t.Fatal(err)
	}
	corefile := nestedString(t, find(t, objs, "ConfigMap", "coredns"), "data", "Corefile")
	if !strings.Contains(corefile, "example.test") {
		t.Fatalf("corefile %s", corefile)
	}
	clusterIP := nestedString(t, find(t, objs, "Service", "kube-dns"), "spec", "clusterIP")
	if clusterIP != "10.50.0.25" {
		t.Fatalf("cluster dns %s", clusterIP)
	}
	local, err := decode([]byte(body["local-storage"]))
	if err != nil {
		t.Fatal(err)
	}
	account := find(t, local, "ServiceAccount", "local-path-provisioner-service-account")
	if account.GetNamespace() != "kube-system" {
		t.Fatalf("service account %#v", account.Object)
	}
	deploy := find(t, local, "Deployment", "local-path-provisioner")
	if nestedString(t, deploy, "spec", "template", "spec", "serviceAccountName") != account.GetName() {
		t.Fatalf("provisioner account %#v", deploy.Object)
	}
	class := find(t, local, "StorageClass", "local-path")
	if nestedString(t, class, "metadata", "annotations", "storageclass.kubernetes.io/is-default-class") != "true" {
		t.Fatalf("storage class %#v", class.Object)
	}
	if nestedString(t, class, "provisioner") != "rancher.io/local-path" {
		t.Fatalf("provisioner %#v", class.Object)
	}
	raw := nestedString(t, find(t, local, "ConfigMap", "local-path-config"), "data", "config.json")
	var cfg storageConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.NodePathMap) != 1 || len(cfg.NodePathMap[0].Paths) != 1 || cfg.NodePathMap[0].Paths[0] != srv.DefaultLocalStoragePath {
		t.Fatalf("storage path %#v", cfg)
	}
	metrics, err := decode([]byte(body["metrics-server"]))
	if err != nil {
		t.Fatal(err)
	}
	api := find(t, metrics, "APIService", "v1beta1.metrics.k8s.io")
	if nestedString(t, api, "spec", "group") != "metrics.k8s.io" || nestedString(t, api, "spec", "version") != "v1beta1" {
		t.Fatalf("metrics api %#v", api.Object)
	}
}

func TestRISCVSkipsMetricsAndCertManager(t *testing.T) {
	prev := stageArch
	stageArch = "riscv64"
	t.Cleanup(func() { stageArch = prev })

	srv := config.Defaults()
	docs, err := addonManifests(srv)
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range docs {
		on := doc.name != "metrics-server"
		if doc.enabled != on {
			t.Fatalf("%s enabled %v", doc.name, doc.enabled)
		}
	}
	charts, err := helmChartManifests(srv)
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range charts {
		on := doc.name != "cert-manager"
		if doc.enabled != on {
			t.Fatalf("%s enabled %v", doc.name, doc.enabled)
		}
	}
	body, skip, err := clusterIssuer(srv)
	if err != nil {
		t.Fatal(err)
	}
	if skip || len(strings.TrimSpace(string(body))) != 0 {
		t.Fatalf("issuer %q skip %v", body, skip)
	}
	srv.ClusterIssuerEmail = "ops@example.com"
	body, skip, err = clusterIssuer(srv)
	if err != nil {
		t.Fatal(err)
	}
	if !skip || len(strings.TrimSpace(string(body))) != 0 {
		t.Fatalf("issuer %q skip %v", body, skip)
	}
}

func TestDisableDropsOnlyThatAddon(t *testing.T) {
	base := config.Defaults()
	base.DisableHelmController = true
	base.Disable = []string{"traefik", "cert-manager", "servicelb"}
	kept, err := addonManifests(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range kept {
		if !doc.enabled {
			t.Fatalf("%s disabled by an unrelated flag", doc.name)
		}
	}
	for _, name := range []string{"coredns", "local-storage", "metrics-server"} {
		srv := base
		srv.Disable = []string{name}
		docs, err := addonManifests(srv)
		if err != nil {
			t.Fatal(err)
		}
		for _, doc := range docs {
			if doc.name == name && doc.enabled {
				t.Fatalf("%s stayed enabled", name)
			}
			if doc.name != name && !doc.enabled {
				t.Fatalf("disabling %s dropped %s", name, doc.name)
			}
			if len(doc.body) == 0 {
				t.Fatalf("%s has an empty manifest", doc.name)
			}
		}
	}
}

func TestHelmChartCRDPrecedesChartObjects(t *testing.T) {
	srv := config.Defaults()
	crd, err := HelmChartCRD(srv)
	if err != nil {
		t.Fatal(err)
	}
	manifests, err := addonManifests(srv)
	if err != nil {
		t.Fatal(err)
	}
	extra := []byte("apiVersion: helm.cattle.io/v1\nkind: HelmChart\nmetadata:\n  name: sample\n  namespace: kube-system\nspec:\n  chart: https://kubernetes.default/static/charts/sample.tgz\n")
	var sawCRD bool
	var sawChart bool
	charts, err := helmChartManifests(srv)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range enabledBodies(manifests, crd, charts, extra) {
		objs, err := decode(body)
		if err != nil {
			t.Fatal(err)
		}
		for _, obj := range objs {
			switch {
			case obj.GetKind() == "HelmChart":
				if !sawCRD {
					t.Fatal("HelmChart was applied before its CRD")
				}
				sawChart = true
			case obj.GetKind() == "CustomResourceDefinition" && obj.GetName() == "helmcharts.helm.cattle.io":
				if sawChart {
					t.Fatal("HelmChart CRD followed a HelmChart")
				}
				if obj.GetAPIVersion() != "apiextensions.k8s.io/v1" {
					t.Fatalf("crd version %s", obj.GetAPIVersion())
				}
				sawCRD = true
			}
		}
	}
	if !sawCRD || !sawChart {
		t.Fatalf("crd %v chart %v", sawCRD, sawChart)
	}
	disabled := srv
	disabled.DisableHelmController = true
	crd, err = HelmChartCRD(disabled)
	if err != nil {
		t.Fatal(err)
	}
	if len(crd) != 0 {
		t.Fatal("helm chart API was embedded while the controller is disabled")
	}
	charts, err = helmChartManifests(disabled)
	if err != nil {
		t.Fatal(err)
	}
	if len(charts) != 0 {
		t.Fatal("disabled controller still stages charts")
	}
	for _, body := range enabledBodies(manifests, crd, charts, nil) {
		if strings.Contains(string(body), "helm.cattle.io") {
			t.Fatal("disabled controller still stages the helm chart API")
		}
	}
}

func TestIPv6ClusterDNS(t *testing.T) {
	srv := config.Defaults()
	srv.ClusterDNS = "2001:db8::10"
	docs, err := addonManifests(srv)
	if err != nil {
		t.Fatal(err)
	}
	var coredns string
	for _, doc := range docs {
		if doc.name == "coredns" {
			coredns = string(doc.body)
		}
	}
	if !strings.Contains(coredns, "2001:db8::10") {
		t.Fatal("cluster dns was not rendered")
	}
}

type storagePath struct {
	Paths []string `json:"paths"`
}

type storageConfig struct {
	NodePathMap []storagePath `json:"nodePathMap"`
}

func find(t *testing.T, objs []*unstructured.Unstructured, kind, name string) *unstructured.Unstructured {
	t.Helper()
	for _, obj := range objs {
		if obj.GetKind() == kind && obj.GetName() == name {
			return obj
		}
	}
	t.Fatalf("missing %s %s", kind, name)
	return nil
}

func nestedString(t *testing.T, obj *unstructured.Unstructured, fields ...string) string {
	t.Helper()
	value, found, err := unstructured.NestedString(obj.Object, fields...)
	if err != nil || !found {
		t.Fatalf("%s %v: %v", obj.GetName(), fields, err)
	}
	return value
}
