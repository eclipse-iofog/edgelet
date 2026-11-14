package control

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet-kube/internal/config"
	"github.com/eclipse-iofog/edgelet-kube/internal/datastore"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func TestNodeIPForServices(t *testing.T) {
	srv := config.Defaults()
	srv.AdvertiseAddress = "198.51.100.8"
	srv.NodeIP = "192.0.2.10"
	srv.NodeExternalIP = "203.0.113.5"
	got, err := nodeIP(srv)
	if err != nil {
		t.Fatal(err)
	}
	if got != "192.0.2.10" {
		t.Fatalf("address %s", got)
	}
	srv.NodeIP = ""
	orig := nodeIPForKubelet
	t.Cleanup(func() { nodeIPForKubelet = orig })
	nodeIPForKubelet = func(string) (net.IP, error) {
		return net.ParseIP("192.0.2.11"), nil
	}
	got, err = nodeIP(srv)
	if err != nil {
		t.Fatal(err)
	}
	if got != "192.0.2.11" {
		t.Fatalf("address %s", got)
	}
	srv.NodeIP = "not-an-ip"
	if _, err := nodeIP(srv); err == nil {
		t.Fatal("accepted a bad node IP")
	}
}

func TestServiceLB(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip(runtime.GOOS)
	}
	// The API processes can be started once in a process. Each case runs
	// in its own process so this test can share the package with the other
	// API startup test.
	mode := os.Getenv("EDGELET_KUBE_SERVICE_LB")
	if mode == "" {
		for _, mode := range []string{"on", "off"} {
			t.Run(mode, func(t *testing.T) {
				cmd := exec.Command(os.Args[0], "-test.run=^TestServiceLB$", "-test.count=1", "-test.timeout=10m")
				cmd.Env = append(os.Environ(), "EDGELET_KUBE_SERVICE_LB="+mode)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("%v\n%s", err, out)
				}
			})
		}
		return
	}
	switch mode {
	case "on":
		testServiceLBOn(t)
	case "off":
		testServiceLBOff(t)
	default:
		t.Fatalf("unknown service load balancer case %s", mode)
	}
}

func testServiceLBOn(t *testing.T) {
	var started atomic.Int32
	orig := startLoadBalancer
	startLoadBalancer = func(ctx context.Context, kubeconfig, address string) error {
		started.Add(1)
		return orig(ctx, kubeconfig, address)
	}
	t.Cleanup(func() { startLoadBalancer = orig })

	client := startControl(t, nil)
	if !waitCount(&started, 1, 5*time.Second) {
		t.Fatal("load balancer controller was not started")
	}
	createService(t, client, loadBalancerService("web"))
	createService(t, client, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "default"},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: map[string]string{"app": "cluster"},
			Ports:    []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt(80)}},
		},
	})
	waitIngress(t, client, "web", "192.0.2.10", true)
	cluster, err := client.CoreV1().Services("default").Get(t.Context(), "cluster", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cluster.Status.LoadBalancer.Ingress) != 0 {
		t.Fatalf("cluster ingress %v", cluster.Status.LoadBalancer.Ingress)
	}
	assertNoLoadBalancerWorkload(t, client)
	if loadBalancerProcess() {
		t.Fatal("load balancer process is running")
	}
}

func testServiceLBOff(t *testing.T) {
	var started atomic.Int32
	orig := startLoadBalancer
	startLoadBalancer = func(ctx context.Context, kubeconfig, address string) error {
		started.Add(1)
		return orig(ctx, kubeconfig, address)
	}
	t.Cleanup(func() { startLoadBalancer = orig })

	client := startControl(t, []string{"servicelb"})
	if waitCount(&started, 1, 300*time.Millisecond) {
		t.Fatal("load balancer controller was started")
	}
	createService(t, client, loadBalancerService("web"))
	waitIngress(t, client, "web", "192.0.2.10", false)
	assertNoLoadBalancerWorkload(t, client)
	if loadBalancerProcess() {
		t.Fatal("load balancer process is running")
	}
}

func waitCount(started *atomic.Int32, want int32, d time.Duration) bool {
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for started.Load() != want {
		select {
		case <-deadline.C:
			return false
		case <-ticker.C:
		}
	}
	return true
}

func startControl(t *testing.T, disable []string) kubernetes.Interface {
	t.Helper()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	store, err := datastore.Open(ctx, datastore.Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	srv := config.Defaults()
	srv.DataDir = dir
	srv.WriteKubeconfig = filepath.Join(dir, "edgelet-kube.yaml")
	srv.BindAddress = "127.0.0.1"
	srv.HTTPSListenPort = freePort(t)
	srv.AdvertiseAddress = "198.51.100.8"
	srv.NodeIP = "192.0.2.10"
	srv.NodeExternalIP = "203.0.113.5"
	srv.ClusterCIDR = "10.20.0.0/16"
	srv.ServiceCIDR = "10.50.0.0/16"
	srv.ClusterDNS = "10.50.0.25"
	srv.ClusterDomain = "example.test"
	srv.NodePodCIDR = "10.20.0.0/24"
	srv.DefaultLocalStoragePath = filepath.Join(dir, "volumes")
	srv.Disable = disable
	rt, err := Start(ctx, Config{
		Server:     srv,
		Store:      store,
		CNIConfDir: filepath.Join(dir, "net.d"),
		CNIBinDir:  filepath.Join(dir, "bin"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Stop)
	cfg, err := clientcmd.BuildConfigFromFlags("", rt.Kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func createService(t *testing.T, client kubernetes.Interface, svc *corev1.Service) {
	t.Helper()
	if _, err := client.CoreV1().Services(svc.Namespace).Create(t.Context(), svc, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func loadBalancerService(name string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeLoadBalancer,
			Selector: map[string]string{"app": name},
			Ports:    []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromInt(8080), Protocol: corev1.ProtocolTCP}},
		},
	}
}

func waitIngress(t *testing.T, client kubernetes.Interface, name, ip string, assigned bool) {
	t.Helper()
	ctx := t.Context()
	wait := 10 * time.Second
	if assigned {
		wait = 30 * time.Second
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var last string
	for {
		svc, err := client.CoreV1().Services("default").Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			last = err.Error()
		} else if assigned {
			ingress := svc.Status.LoadBalancer.Ingress
			if len(ingress) == 1 && ingress[0].IP == ip && ingress[0].Hostname == "" {
				return
			}
			last = ingressString(ingress)
		} else if len(svc.Status.LoadBalancer.Ingress) > 0 {
			t.Fatalf("ingress %v", svc.Status.LoadBalancer.Ingress)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-deadline.C:
			if assigned {
				t.Fatalf("ingress %s", last)
			}
			return
		case <-ticker.C:
		}
	}
}

func ingressString(ingress []corev1.LoadBalancerIngress) string {
	if len(ingress) == 0 {
		return "pending"
	}
	parts := make([]string, len(ingress))
	for i, item := range ingress {
		parts[i] = item.IP
		if item.Hostname != "" {
			parts[i] = item.Hostname
		}
	}
	return strings.Join(parts, ",")
}

func assertNoLoadBalancerWorkload(t *testing.T, client kubernetes.Interface) {
	t.Helper()
	ctx := t.Context()
	sets, err := client.AppsV1().DaemonSets(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sets.Items) != 0 {
		names := make([]string, len(sets.Items))
		for i, set := range sets.Items {
			names[i] = set.Namespace + "/" + set.Name
		}
		t.Fatalf("daemonsets %s", strings.Join(names, ", "))
	}
	pods, err := client.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, pod := range pods.Items {
		if loadBalancerWorkload(pod.Name, images(pod)) {
			t.Fatalf("pod %s/%s", pod.Namespace, pod.Name)
		}
	}
}

func images(pod corev1.Pod) []string {
	out := make([]string, 0, len(pod.Spec.Containers))
	for _, container := range pod.Spec.Containers {
		out = append(out, container.Image)
	}
	return out
}

func loadBalancerWorkload(name string, images []string) bool {
	fields := append([]string{name}, images...)
	for _, field := range fields {
		field = strings.ToLower(field)
		if strings.Contains(field, "klipper-lb") || strings.Contains(field, "svclb") {
			return true
		}
	}
	return false
}

func loadBalancerProcess() bool {
	return processNamed("klipper-lb", "klipper", "svclb")
}

func processNamed(names ...string) bool {
	want := map[string]struct{}{}
	for _, name := range names {
		want[name] = struct{}{}
	}
	if _, err := os.Stat("/proc"); err == nil {
		entries, err := os.ReadDir("/proc")
		if err != nil {
			return false
		}
		for _, entry := range entries {
			body, err := os.ReadFile("/proc/" + entry.Name() + "/cmdline")
			if err != nil || len(body) == 0 {
				continue
			}
			argv0 := string(body)
			if i := strings.IndexByte(argv0, 0); i >= 0 {
				argv0 = argv0[:i]
			}
			if _, ok := want[filepath.Base(argv0)]; ok {
				return true
			}
		}
		return false
	}
	out, err := exec.Command("ps", "-ax", "-o", "comm=").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if _, ok := want[filepath.Base(strings.TrimSpace(line))]; ok {
			return true
		}
	}
	return false
}
