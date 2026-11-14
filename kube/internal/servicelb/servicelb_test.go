package servicelb

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

func TestAssignsNodeIPWithoutDaemonSet(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client := fake.NewSimpleClientset()
	if _, err := client.CoreV1().Services("default").Create(ctx, loadBalancer("web"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Services("default").Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "default"},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: map[string]string{"app": "cluster"},
			Ports:    []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt(80)}},
		},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- run(ctx, client, "192.0.2.10") }()
	waitPublished(t, client, "web", "192.0.2.10", true)
	cluster, err := client.CoreV1().Services("default").Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cluster.Status.LoadBalancer.Ingress) != 0 {
		t.Fatalf("cluster ingress %v", cluster.Status.LoadBalancer.Ingress)
	}

	web, err := client.CoreV1().Services("default").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	web.Spec.Type = corev1.ServiceTypeClusterIP
	if _, err := client.CoreV1().Services("default").Update(ctx, web, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitPublished(t, client, "web", "192.0.2.10", false)

	sets, err := client.AppsV1().DaemonSets(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sets.Items) != 0 {
		t.Fatalf("daemonsets %#v", sets.Items)
	}
	for _, action := range client.Actions() {
		switch action.GetVerb() {
		case "create", "update", "patch":
		default:
			continue
		}
		resource := action.GetResource().Resource
		if resource == "daemonsets" || resource == "pods" {
			t.Fatalf("%s %s", action.GetVerb(), resource)
		}
	}
	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}
}

func TestRunRejectsBadAddress(t *testing.T) {
	if err := Run(context.Background(), "", "not-an-ip"); err == nil {
		t.Fatal("accepted a bad node IP")
	}
}

func loadBalancer(name string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeLoadBalancer,
			Selector: map[string]string{"app": name},
			Ports:    []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromInt(8080), Protocol: corev1.ProtocolTCP}},
		},
	}
}

func waitPublished(t *testing.T, client kubernetes.Interface, name, ip string, assigned bool) {
	t.Helper()
	ctx := t.Context()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		svc, err := client.CoreV1().Services("default").Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		got := published(svc, ip)
		if assigned && got {
			return
		}
		if !assigned && len(svc.Status.LoadBalancer.Ingress) == 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-deadline.C:
			t.Fatalf("ingress %v", svc.Status.LoadBalancer.Ingress)
		case <-ticker.C:
		}
	}
}
