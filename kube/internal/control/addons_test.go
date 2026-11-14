package control

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet-kube/internal/config"
	"github.com/eclipse-iofog/edgelet-kube/internal/kubectl"
	"github.com/eclipse-iofog/edgelet-kube/scripts/airgap"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	aggregator "k8s.io/kube-aggregator/pkg/client/clientset_generated/clientset"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

type clusterClients struct {
	kube kubernetes.Interface
	reg  aggregator.Interface
}

func assertAddons(t *testing.T, client clusterClients, srv config.Server, coredns, storage, metrics bool) {
	t.Helper()
	ctx := t.Context()
	if coredns {
		dep, err := client.kube.AppsV1().Deployments("kube-system").Get(ctx, "coredns", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(dep.Spec.Template.Spec.Containers) != 1 || !pinnedImage(dep.Spec.Template.Spec.Containers[0].Image, "coredns/coredns") {
			t.Fatalf("coredns image %v", dep.Spec.Template.Spec.Containers)
		}
		cm, err := client.kube.CoreV1().ConfigMaps("kube-system").Get(ctx, "coredns", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(cm.Data["Corefile"], srv.ClusterDomain) {
			t.Fatalf("corefile %s", cm.Data["Corefile"])
		}
		svc, err := client.kube.CoreV1().Services("kube-system").Get(ctx, "kube-dns", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if svc.Spec.ClusterIP != srv.ClusterDNS {
			t.Fatalf("cluster dns %s", svc.Spec.ClusterIP)
		}
	} else {
		waitGone(t, func(ctx context.Context) error {
			_, err := client.kube.AppsV1().Deployments("kube-system").Get(ctx, "coredns", metav1.GetOptions{})
			return err
		})
	}

	if storage {
		class, err := client.kube.StorageV1().StorageClasses().Get(ctx, "local-path", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if class.Provisioner != "rancher.io/local-path" || class.Annotations["storageclass.kubernetes.io/is-default-class"] != "true" {
			t.Fatalf("storage class %#v", class)
		}
		cm, err := client.kube.CoreV1().ConfigMaps("kube-system").Get(ctx, "local-path-config", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(cm.Data["config.json"], srv.DefaultLocalStoragePath) {
			t.Fatalf("storage path %s", cm.Data["config.json"])
		}
	} else {
		waitGone(t, func(ctx context.Context) error {
			_, err := client.kube.StorageV1().StorageClasses().Get(ctx, "local-path", metav1.GetOptions{})
			return err
		})
	}

	if metrics {
		api, err := client.reg.ApiregistrationV1().APIServices().Get(ctx, "v1beta1.metrics.k8s.io", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if api.Spec.Group != "metrics.k8s.io" || api.Spec.Version != "v1beta1" {
			t.Fatalf("metrics api %#v", api.Spec)
		}
	} else {
		waitGone(t, func(ctx context.Context) error {
			_, err := client.reg.ApiregistrationV1().APIServices().Get(ctx, "v1beta1.metrics.k8s.io", metav1.GetOptions{})
			return err
		})
	}
}

func waitGone(t *testing.T, get func(context.Context) error) {
	t.Helper()
	ctx := t.Context()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var last error
	for {
		last = get(ctx)
		if apierrors.IsNotFound(last) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-deadline.C:
			t.Fatalf("object still present: %v", last)
		case <-ticker.C:
		}
	}
}

func pinnedImage(got, repo string) bool {
	for _, ref := range airgap.Names("") {
		name, _, _ := strings.Cut(ref, "@")
		image, _, _ := strings.Cut(name, ":")
		if strings.HasSuffix(image, "/"+repo) && got == ref {
			return true
		}
	}
	return false
}

func assertMetricsTop(t *testing.T, client clusterClients, kubeconfig string) {
	t.Helper()
	ctx := t.Context()
	const node = "edge-a"
	if _, err := client.kube.CoreV1().Nodes().Create(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: node}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	server, address := metricsServer(t, node)
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	ready := true
	number := int32(port)
	name := "https"
	protocol := corev1.ProtocolTCP
	if _, err := client.kube.DiscoveryV1().EndpointSlices("kube-system").Create(ctx, &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "metrics-server-local",
			Namespace: "kube-system",
			Labels:    map[string]string{discoveryv1.LabelServiceName: "metrics-server"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{{
			Addresses:  []string{address},
			Conditions: discoveryv1.EndpointConditions{Ready: &ready},
		}},
		Ports: []discoveryv1.EndpointPort{{
			Name:     &name,
			Port:     &number,
			Protocol: &protocol,
		}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitMetricsAvailable(t, client)

	t.Setenv("KUBECONFIG", kubeconfig)
	stdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	err = kubectl.Run([]string{"top", "nodes"})
	_ = writer.Close()
	os.Stdout = stdout
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, reader)
	_ = reader.Close()
	if err != nil {
		t.Fatalf("kubectl top: %v\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), node) {
		t.Fatalf("kubectl top output %s", buf.String())
	}
}

func waitMetricsAvailable(t *testing.T, client clusterClients) {
	t.Helper()
	ctx := t.Context()
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var last string
	for {
		api, err := client.reg.ApiregistrationV1().APIServices().Get(ctx, "v1beta1.metrics.k8s.io", metav1.GetOptions{})
		if err != nil {
			last = err.Error()
		} else {
			last = "waiting"
			for _, cond := range api.Status.Conditions {
				if cond.Type == "Available" {
					last = string(cond.Reason) + ": " + cond.Message
					if cond.Status == "True" {
						return
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-deadline.C:
			t.Fatalf("metrics api: %s", last)
		case <-ticker.C:
		}
	}
}

func metricsServer(t *testing.T, node string) (*httptest.Server, string) {
	t.Helper()
	scheme := kruntime.NewScheme()
	if err := metricsv1beta1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	codecs := serializer.NewCodecFactory(scheme)
	list := &metricsv1beta1.NodeMetricsList{
		Items: []metricsv1beta1.NodeMetrics{{
			ObjectMeta: metav1.ObjectMeta{Name: node},
			Timestamp:  metav1.Now(),
			Window:     metav1.Duration{Duration: 15 * time.Second},
			Usage: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10m"),
				corev1.ResourceMemory: resource.MustParse("10Mi"),
			},
		}},
	}
	address := outboundIPv4(t)
	listener, err := net.Listen("tcp", net.JoinHostPort(address, "0"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimSuffix(r.URL.Path, "/")
		if path != "/apis/metrics.k8s.io/v1beta1/nodes" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"metrics.k8s.io/v1beta1","resources":[{"name":"nodes","singularName":"","namespaced":false,"kind":"NodeMetrics","verbs":["get","list"]}]}`))
			return
		}
		info := serializerFor(r.Header.Get("Accept"), codecs.SupportedMediaTypes())
		w.Header().Set("Content-Type", info.MediaType)
		encoded := info.Serializer
		if encoded == nil && info.StreamSerializer != nil {
			encoded = info.StreamSerializer.Serializer
		}
		if encoded == nil {
			http.Error(w, "no serializer", http.StatusNotAcceptable)
			return
		}
		if err := codecs.EncoderForVersion(encoded, metricsv1beta1.SchemeGroupVersion).Encode(list, w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}))
	previous := server.Listener
	server.Listener = listener
	_ = previous.Close()
	server.StartTLS()
	return server, address
}

func outboundIPv4(t *testing.T) string {
	t.Helper()
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	host, _, err := net.SplitHostPort(conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	if host == "" || host == "127.0.0.1" {
		t.Fatal("no routable address for the metrics service")
	}
	return host
}

func serializerFor(accept string, types []kruntime.SerializerInfo) kruntime.SerializerInfo {
	for _, part := range strings.Split(accept, ",") {
		media := strings.TrimSpace(part)
		if cut, _, ok := strings.Cut(media, ";"); ok {
			media = strings.TrimSpace(cut)
		}
		if media == "" || media == "*/*" {
			continue
		}
		if info, ok := kruntime.SerializerInfoForMediaType(types, media); ok {
			return info
		}
	}
	for _, info := range types {
		if info.MediaType == "application/json" {
			return info
		}
	}
	return types[0]
}

func kubeClient(t *testing.T, kubeconfig string) clusterClients {
	t.Helper()
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := aggregator.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return clusterClients{kube: kube, reg: reg}
}
