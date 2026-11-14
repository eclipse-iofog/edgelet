package control

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet-kube/internal/config"
	"github.com/eclipse-iofog/edgelet-kube/internal/datastore"
	"github.com/eclipse-iofog/edgelet-kube/internal/stage"
	"k8s.io/client-go/kubernetes"
)

func TestHelmControllerDisabledKeepsManifestAddons(t *testing.T) {
	if os.Getenv("EDGELET_KUBE_HELM_OFF") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelmControllerDisabledKeepsManifestAddons$", "-test.count=1", "-test.timeout=4m")
		cmd.Env = append(os.Environ(), "EDGELET_KUBE_HELM_OFF=1")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), helmOffChecked) {
			t.Fatalf("%v\n%s", err, out)
		}
		return
	}
	srv := config.Defaults()
	srv.DisableHelmController = true
	srv.ClusterIssuerEmail = "ops@example.com"
	rt := bootServer(t, &srv)
	ctx := t.Context()
	client := kubeClient(t, rt.Kubeconfig)
	assertAddons(t, client, srv, true, true, true)
	_, err := client.kube.Discovery().ServerResourcesForGroupVersion("helm.cattle.io/v1")
	if err == nil {
		t.Fatal("helm chart API is served")
	}
	if err := client.kube.Discovery().RESTClient().Get().AbsPath("/apis/cert-manager.io/v1/clusterissuers/letsencrypt-prod").Do(ctx).Error(); err == nil {
		t.Fatal("letsencrypt cluster issuer was written")
	}
	if err := client.kube.Discovery().RESTClient().Get().AbsPath("/apis/cert-manager.io/v1/clusterissuers/selfsigned").Do(ctx).Error(); err == nil {
		t.Fatal("selfsigned cluster issuer was written")
	}
	fmt.Println(helmOffChecked)
}

const helmOffChecked = "helm-controller-disabled-checked"

func assertIssuerRetries(ctx context.Context, t *testing.T, kubeconfig string, srv config.Server) {
	t.Helper()
	withEmail := srv
	withEmail.ClusterIssuerEmail = "ops@example.com"
	done := make(chan error, 1)
	go func() {
		done <- stage.ApplyIssuer(ctx, kubeconfig, withEmail)
	}()
	client := kubeClient(t, kubeconfig).kube
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if issuerPresent(ctx, client) {
			t.Fatal("cluster issuer was written before its CRD")
		}
		select {
		case err := <-done:
			t.Fatalf("issuer apply returned before the CRD: %v", err)
		default:
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := stage.Apply(ctx, kubeconfig, srv, []byte(clusterIssuerCRD)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("cluster issuer was not applied")
	}
	body, err := client.Discovery().RESTClient().Get().AbsPath("/apis/cert-manager.io/v1/clusterissuers/letsencrypt-prod").DoRaw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(body)
	for _, want := range []string{
		"ops@example.com",
		"letsencrypt-prod-account-key",
		"https://acme-v02.api.letsencrypt.org/directory",
		"traefik",
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("issuer missing %s\n%s", want, raw)
		}
	}
	signed, err := client.Discovery().RESTClient().Get().AbsPath("/apis/cert-manager.io/v1/clusterissuers/selfsigned").DoRaw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(signed), "selfSigned") {
		t.Fatalf("selfsigned issuer\n%s", signed)
	}
}

func bootServer(t *testing.T, srv *config.Server) *Runtime {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip(runtime.GOOS)
	}
	// The socket path has to stay under the platform limit. The test name is
	// part of t.TempDir and makes that path too long.
	dir, err := os.MkdirTemp("/tmp", "ek")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	store, err := datastore.Open(ctx, datastore.Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	srv.DataDir = dir
	srv.WriteKubeconfig = filepath.Join(dir, "edgelet-kube.yaml")
	srv.BindAddress = "127.0.0.1"
	srv.HTTPSListenPort = freePort(t)
	srv.NodeIP = "192.0.2.10"
	srv.ClusterCIDR = "10.20.0.0/16"
	srv.ServiceCIDR = "10.50.0.0/16"
	srv.ClusterDNS = "10.50.0.25"
	srv.ClusterDomain = "example.test"
	srv.NodePodCIDR = "10.20.0.0/24"
	srv.DefaultLocalStoragePath = filepath.Join(dir, "volumes")
	rt, err := Start(ctx, Config{
		Server:     *srv,
		Store:      store,
		CNIConfDir: filepath.Join(dir, "net.d"),
		CNIBinDir:  filepath.Join(dir, "bin"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Stop)
	return rt
}

func issuerPresent(ctx context.Context, client kubernetes.Interface) bool {
	err := client.Discovery().RESTClient().Get().AbsPath("/apis/cert-manager.io/v1/clusterissuers/letsencrypt-prod").Do(ctx).Error()
	return err == nil
}

const clusterIssuerCRD = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: clusterissuers.cert-manager.io
spec:
  group: cert-manager.io
  names:
    kind: ClusterIssuer
    listKind: ClusterIssuerList
    plural: clusterissuers
    singular: clusterissuer
  scope: Cluster
  versions:
  - name: v1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
        x-kubernetes-preserve-unknown-fields: true
`
