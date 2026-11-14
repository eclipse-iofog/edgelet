package helm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet-kube/internal/config"
	"github.com/eclipse-iofog/edgelet-kube/scripts/airgap"
)

func TestJobImageUsesPin(t *testing.T) {
	var want string
	for _, ref := range airgap.Images() {
		name, _, _ := strings.Cut(ref, "@")
		repo, _, ok := strings.Cut(name, ":")
		if ok && strings.HasSuffix(repo, "rancher/klipper-helm") {
			want = ref
		}
	}
	if want == "" {
		t.Fatal("klipper-helm is not on the image list")
	}
	got, err := jobImage("")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("job image %s", got)
	}
	prefixed, err := jobImage("registry.example:5000")
	if err != nil {
		t.Fatal(err)
	}
	if prefixed != "registry.example:5000/"+want {
		t.Fatalf("job image %s", prefixed)
	}
	srv := config.Defaults()
	srv.SystemDefaultRegistry = "registry.example:5000"
	opts, port, err := controllerOptions(srv)
	if err != nil {
		t.Fatal(err)
	}
	if opts.DefaultJobImage != prefixed {
		t.Fatalf("controller image %s", opts.DefaultJobImage)
	}
	if port != "6443" {
		t.Fatalf("port %s", port)
	}
}

func TestControllerStaysDownWithoutChartAPI(t *testing.T) {
	called := false
	orig := startLeading
	startLeading = func(context.Context, string, config.Server) error {
		called = true
		return nil
	}
	t.Cleanup(func() { startLeading = orig })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := Run(ctx, writeKubeconfig(t, server.URL), config.Defaults())
	if err == nil {
		t.Fatal("expected the chart API to be absent")
	}
	if called {
		t.Fatal("controller started without the chart API")
	}
}

func TestControllerStartsAfterChartAPI(t *testing.T) {
	called := false
	orig := startLeading
	startLeading = func(context.Context, string, config.Server) error {
		called = true
		return nil
	}
	t.Cleanup(func() { startLeading = orig })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "helm.cattle.io/v1") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"helm.cattle.io/v1","resources":[{"name":"helmcharts","singularName":"helmchart","namespaced":true,"kind":"HelmChart","verbs":["get","list","watch"]},{"name":"helmchartconfigs","singularName":"helmchartconfig","namespaced":true,"kind":"HelmChartConfig","verbs":["get","list","watch"]}]}`))
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := Run(ctx, writeKubeconfig(t, server.URL), config.Defaults()); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("controller was not started")
	}
}

func writeKubeconfig(t *testing.T, server string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig")
	body := "apiVersion: v1\nkind: Config\nclusters:\n- name: t\n  cluster:\n    server: " + server + "\n    insecure-skip-tls-verify: true\nusers:\n- name: t\n  user: {}\ncontexts:\n- name: t\n  context:\n    cluster: t\n    user: t\ncurrent-context: t\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
