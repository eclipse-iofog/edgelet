package stage

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet-kube/internal/charts"
	"github.com/eclipse-iofog/edgelet-kube/internal/config"
	"github.com/eclipse-iofog/edgelet-kube/scripts/airgap"
	"go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"
)

func TestHelmChartsUseLocalURLs(t *testing.T) {
	srv := config.Defaults()
	srv.SystemDefaultRegistry = "registry.example:5000"
	docs, err := helmChartManifests(srv)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 3 {
		t.Fatalf("charts %d", len(docs))
	}
	body := map[string]string{}
	for _, doc := range docs {
		if !doc.enabled {
			t.Fatalf("%s disabled", doc.name)
		}
		text := string(doc.body)
		for _, absent := range []string{"klipper-lb", "datasance", "potctl", "iofogctl"} {
			if strings.Contains(text, absent) {
				t.Fatalf("%s contains %s", doc.name, absent)
			}
		}
		body[doc.name] = text
	}
	names, err := charts.Names()
	if err != nil {
		t.Fatal(err)
	}
	objs, err := decode([]byte(body["traefik"]))
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 {
		t.Fatalf("traefik documents %d", len(objs))
	}
	crdChart := find(t, objs, "HelmChart", "traefik-crd")
	traefik := find(t, objs, "HelmChart", "traefik")
	if crdChart.GetNamespace() != "kube-system" || traefik.GetNamespace() != "kube-system" {
		t.Fatal("charts are not in kube-system")
	}
	assertChartURL(t, nestedString(t, crdChart, "spec", "chart"), names, "traefik-crd")
	assertChartURL(t, nestedString(t, traefik, "spec", "chart"), names, "traefik")
	var values traefikChartValues
	if err := yaml.Unmarshal([]byte(nestedString(t, traefik, "spec", "valuesContent")), &values); err != nil {
		t.Fatal(err)
	}
	if values.Service.Spec.Type != "LoadBalancer" {
		t.Fatalf("service %s", values.Service.Spec.Type)
	}
	if values.Ports.Web.ExposedPort != 80 || values.Ports.Websecure.ExposedPort != 443 {
		t.Fatalf("ports %#v", values.Ports)
	}
	ref, err := imageRef(srv.SystemDefaultRegistry, "library/traefik")
	if err != nil {
		t.Fatal(err)
	}
	image := values.Image.Registry + "/" + values.Image.Repository + ":" + values.Image.Tag + "@" + values.Image.Digest
	if image != ref {
		t.Fatalf("image %s", image)
	}

	certObjs, err := decode([]byte(body["cert-manager"]))
	if err != nil {
		t.Fatal(err)
	}
	cert := find(t, certObjs, "HelmChart", "cert-manager")
	assertChartURL(t, nestedString(t, cert, "spec", "chart"), names, "cert-manager")
	if nestedString(t, cert, "spec", "targetNamespace") != "cert-manager" {
		t.Fatalf("namespace %#v", cert.Object)
	}
	var certValues certManagerChartValues
	if err := yaml.Unmarshal([]byte(nestedString(t, cert, "spec", "valuesContent")), &certValues); err != nil {
		t.Fatal(err)
	}
	if !certValues.CRDs.Enabled {
		t.Fatal("crds are disabled")
	}
	for _, got := range []struct {
		repo  string
		image chartImageValue
	}{
		{"cert-manager-controller", certValues.Image},
		{"cert-manager-webhook", certValues.Webhook.Image},
		{"cert-manager-cainjector", certValues.Cainjector.Image},
		{"cert-manager-startupapicheck", certValues.Startup.Image},
		{"cert-manager-acmesolver", certValues.Solver.Image},
	} {
		want, err := imageRef(srv.SystemDefaultRegistry, got.repo)
		if err != nil {
			t.Fatal(err)
		}
		if got.image.Repository+":"+got.image.Tag+"@"+got.image.Digest != want {
			t.Fatalf("%s image %s", got.repo, got.image.Repository)
		}
		if !strings.HasPrefix(got.image.Repository, "registry.example:5000/") {
			t.Fatalf("%s registry %s", got.repo, got.image.Repository)
		}
	}
}

func TestDisableSkipsOnlyThatChart(t *testing.T) {
	base := config.Defaults()
	base.Disable = []string{"servicelb"}
	kept, err := helmChartManifests(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range kept {
		if !doc.enabled {
			t.Fatalf("%s disabled by servicelb", doc.name)
		}
	}
	for _, name := range []string{"traefik", "gateway-api-crd", "cert-manager"} {
		srv := base
		srv.Disable = []string{name}
		docs, err := helmChartManifests(srv)
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
		}
	}
	off := base
	off.DisableHelmController = true
	docs, err := helmChartManifests(off)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 0 {
		t.Fatalf("charts %d", len(docs))
	}
}

func TestClusterIssuerRules(t *testing.T) {
	var buf bytes.Buffer
	klog.LogToStderr(false)
	klog.SetOutput(&buf)
	t.Cleanup(func() {
		klog.Flush()
		klog.LogToStderr(true)
		klog.SetOutput(os.Stderr)
	})

	empty := config.Defaults()
	empty.ClusterIssuerEmail = "  "
	body, skip, err := clusterIssuer(empty)
	if err != nil || skip {
		t.Fatalf("skip %v err %v", skip, err)
	}
	objs, err := decode(body)
	if err != nil {
		t.Fatal(err)
	}
	assertSelfSigned(t, objs)
	if issuerNamed(objs, "letsencrypt-prod") != nil {
		t.Fatal("letsencrypt issuer written without an email")
	}
	klog.Flush()
	if buf.Len() != 0 {
		t.Fatalf("empty email logged %q", buf.String())
	}

	buf.Reset()
	skipped := empty
	skipped.ClusterIssuerEmail = "ops@example.com"
	skipped.Disable = []string{"traefik", "cert-manager"}
	if err := ApplyIssuer(t.Context(), "", skipped); err != nil {
		t.Fatal(err)
	}
	klog.Flush()
	if strings.Count(buf.String(), "letsencrypt cluster issuer was skipped") != 1 {
		t.Fatalf("log %q", buf.String())
	}

	buf.Reset()
	onlyTraefik := skipped
	onlyTraefik.Disable = []string{"traefik"}
	body, skip, err = clusterIssuer(onlyTraefik)
	if err != nil || !skip {
		t.Fatalf("skip %v err %v", skip, err)
	}
	objs, err = decode(body)
	if err != nil {
		t.Fatal(err)
	}
	assertSelfSigned(t, objs)
	if issuerNamed(objs, "letsencrypt-prod") != nil {
		t.Fatal("letsencrypt issuer written with traefik disabled")
	}

	buf.Reset()
	helmOff := empty
	helmOff.ClusterIssuerEmail = "ops@example.com"
	helmOff.DisableHelmController = true
	body, skip, err = clusterIssuer(helmOff)
	if err != nil || skip || len(bytes.TrimSpace(body)) != 0 {
		t.Fatalf("skip %v body %d err %v", skip, len(body), err)
	}
	if err := ApplyIssuer(t.Context(), "", helmOff); err != nil {
		t.Fatal(err)
	}
	klog.Flush()
	if buf.Len() != 0 {
		t.Fatalf("helm disable logged %q", buf.String())
	}

	buf.Reset()
	loadBalancerOff := empty
	loadBalancerOff.ClusterIssuerEmail = "ops@example.com"
	loadBalancerOff.Disable = []string{"servicelb"}
	body, skip, err = clusterIssuer(loadBalancerOff)
	if err != nil {
		t.Fatal(err)
	}
	if skip || len(body) == 0 || buf.Len() != 0 {
		t.Fatalf("skip %v body %d log %q", skip, len(body), buf.String())
	}
	objs, err = decode(body)
	if err != nil {
		t.Fatal(err)
	}
	assertSelfSigned(t, objs)
	issuer := find(t, objs, "ClusterIssuer", "letsencrypt-prod")
	if issuer.GetAPIVersion() != "cert-manager.io/v1" {
		t.Fatalf("version %s", issuer.GetAPIVersion())
	}
	if nestedString(t, issuer, "spec", "acme", "email") != "ops@example.com" {
		t.Fatalf("email %#v", issuer.Object)
	}
	if nestedString(t, issuer, "spec", "acme", "server") != "https://acme-v02.api.letsencrypt.org/directory" {
		t.Fatalf("server %#v", issuer.Object)
	}
	if nestedString(t, issuer, "spec", "acme", "privateKeySecretRef", "name") != "letsencrypt-prod-account-key" {
		t.Fatalf("key %#v", issuer.Object)
	}
	solvers, found, err := unstructured.NestedSlice(issuer.Object, "spec", "acme", "solvers")
	if err != nil || !found || len(solvers) != 1 {
		t.Fatalf("solvers %#v %v", issuer.Object, err)
	}
	solver, ok := solvers[0].(map[string]any)
	if !ok {
		t.Fatalf("solver %#v", solvers[0])
	}
	class, found, err := unstructured.NestedString(solver, "http01", "ingress", "class")
	if err != nil || !found || class != "traefik" {
		t.Fatalf("class %s %v", class, err)
	}
}

func TestChartVersionsMatchPins(t *testing.T) {
	docs, err := helmChartManifests(config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for _, doc := range docs {
		text += string(doc.body)
	}
	for _, chart := range []string{"traefik", "traefik-crd", "gateway-api-crd", "cert-manager"} {
		version := airgap.ChartVersion(chart)
		if version == "" || !strings.Contains(text, chart+"-"+version+".tgz") {
			t.Fatalf("chart %s version %s", chart, version)
		}
	}
}

type chartImageRef struct {
	Registry   string `yaml:"registry"`
	Repository string `yaml:"repository"`
	Tag        string `yaml:"tag"`
	Digest     string `yaml:"digest"`
}

type serviceTypeSpec struct {
	Type string `yaml:"type"`
}

type serviceSpecBody struct {
	Spec serviceTypeSpec `yaml:"spec"`
}

type exposedPort struct {
	ExposedPort int `yaml:"exposedPort"`
}

type traefikPorts struct {
	Web       exposedPort `yaml:"web"`
	Websecure exposedPort `yaml:"websecure"`
}

type traefikChartValues struct {
	Image   chartImageRef   `yaml:"image"`
	Service serviceSpecBody `yaml:"service"`
	Ports   traefikPorts    `yaml:"ports"`
}

type chartImageValue struct {
	Repository string `yaml:"repository"`
	Tag        string `yaml:"tag"`
	Digest     string `yaml:"digest"`
}

type crdToggle struct {
	Enabled bool `yaml:"enabled"`
}

type chartImageHolder struct {
	Image chartImageValue `yaml:"image"`
}

type certManagerChartValues struct {
	CRDs       crdToggle        `yaml:"crds"`
	Image      chartImageValue  `yaml:"image"`
	Webhook    chartImageHolder `yaml:"webhook"`
	Cainjector chartImageHolder `yaml:"cainjector"`
	Startup    chartImageHolder `yaml:"startupapicheck"`
	Solver     chartImageHolder `yaml:"acmesolver"`
}

func assertSelfSigned(t *testing.T, objs []*unstructured.Unstructured) {
	t.Helper()
	issuer := find(t, objs, "ClusterIssuer", "selfsigned")
	if issuer.GetAPIVersion() != "cert-manager.io/v1" {
		t.Fatalf("version %s", issuer.GetAPIVersion())
	}
	spec, found, err := unstructured.NestedMap(issuer.Object, "spec", "selfSigned")
	if err != nil || !found || len(spec) != 0 {
		t.Fatalf("selfSigned %#v %v", issuer.Object, err)
	}
}

func issuerNamed(objs []*unstructured.Unstructured, name string) *unstructured.Unstructured {
	for _, obj := range objs {
		if obj.GetKind() == "ClusterIssuer" && obj.GetName() == name {
			return obj
		}
	}
	return nil
}

func assertChartURL(t *testing.T, got string, names []string, chart string) {
	t.Helper()
	version := airgap.ChartVersion(chart)
	want := "https://kubernetes.default.svc" + charts.Prefix + chart + "-" + version + ".tgz"
	if got != want {
		t.Fatalf("chart url %s", got)
	}
	found := false
	for _, name := range names {
		if name == chart+"-"+version+".tgz" {
			found = true
		}
	}
	if !found {
		t.Fatalf("embedded charts %v", names)
	}
}
