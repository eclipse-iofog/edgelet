package place

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/client-go/kubernetes/fake"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	core "k8s.io/kubernetes/pkg/apis/core"
)

func TestAdmitAssignsUnconstrainedPod(t *testing.T) {
	pod := plainPod()
	p := admitPlugin(t, readyNode())
	if err := p.Admit(context.Background(), podAttrs(pod, admission.Create), nil); err != nil {
		t.Fatal(err)
	}
	if pod.Spec.NodeName != "node-a" {
		t.Fatalf("nodeName %q", pod.Spec.NodeName)
	}
	if got := eventCount(t, p); got != 0 {
		t.Fatalf("events %d", got)
	}
}

func TestAdmitLeavesOtherHostnameUnbound(t *testing.T) {
	pod := plainPod()
	pod.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": "other"}
	p := admitPlugin(t, readyNode())
	if err := p.Admit(context.Background(), podAttrs(pod, admission.Create), nil); err != nil {
		t.Fatal(err)
	}
	if pod.Spec.NodeName != "" {
		t.Fatalf("nodeName %q", pod.Spec.NodeName)
	}
	events, err := p.client.CoreV1().Events("default").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events.Items) != 1 {
		t.Fatalf("events %d", len(events.Items))
	}
	if events.Items[0].Reason != reasonNodeSelector {
		t.Fatalf("reason %s", events.Items[0].Reason)
	}
	if events.Items[0].Message == "" {
		t.Fatal("empty message")
	}
}

func TestAdmitIgnoresOtherScheduler(t *testing.T) {
	pod := plainPod()
	pod.Spec.SchedulerName = "custom-scheduler"
	p := admitPlugin(t, readyNode())
	if err := p.Admit(context.Background(), podAttrs(pod, admission.Create), nil); err != nil {
		t.Fatal(err)
	}
	if pod.Spec.NodeName != "" {
		t.Fatalf("nodeName %q", pod.Spec.NodeName)
	}
	if got := eventCount(t, p); got != 0 {
		t.Fatalf("events %d", got)
	}
}

func TestAdmitSkipsUpdate(t *testing.T) {
	pod := plainPod()
	p := admitPlugin(t, readyNode())
	if err := p.Admit(context.Background(), podAttrs(pod, admission.Update), nil); err != nil {
		t.Fatal(err)
	}
	if pod.Spec.NodeName != "" {
		t.Fatalf("nodeName %q", pod.Spec.NodeName)
	}
}

func TestAdmitAssignsInternalPod(t *testing.T) {
	pod := &core.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec: core.PodSpec{
			Containers: []core.Container{{Name: "app", Image: "pause"}},
		},
	}
	p := admitPlugin(t, readyNode())
	if err := p.Admit(context.Background(), podAttrs(pod, admission.Create), nil); err != nil {
		t.Fatal(err)
	}
	if pod.Spec.NodeName != "node-a" {
		t.Fatalf("nodeName %q", pod.Spec.NodeName)
	}
}

func TestRegisterAddsPlugin(t *testing.T) {
	plugins := admission.NewPlugins()
	order := Register(plugins, []string{"NodeRestriction"}, "node-a")
	found := false
	for _, name := range order {
		if name == PluginName {
			found = true
		}
	}
	if !found {
		t.Fatalf("order %v", order)
	}
	registered := plugins.Registered()
	found = false
	for _, name := range registered {
		if name == PluginName {
			found = true
		}
	}
	if !found {
		t.Fatalf("registered %v", registered)
	}
}

func admitPlugin(t *testing.T, node *corev1.Node, pods ...runtime.Object) *plugin {
	t.Helper()
	nodes := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	if err := nodes.Add(node); err != nil {
		t.Fatal(err)
	}
	podIndex := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	for _, pod := range pods {
		if err := podIndex.Add(pod); err != nil {
			t.Fatal(err)
		}
	}
	p := newPlugin("node-a")
	p.nodes = corelisters.NewNodeLister(nodes)
	p.pods = corelisters.NewPodLister(podIndex)
	p.client = fake.NewClientset()
	return p
}

func podAttrs(pod runtime.Object, op admission.Operation) admission.Attributes {
	return admission.NewAttributesRecord(
		pod,
		nil,
		pod.GetObjectKind().GroupVersionKind(),
		"default",
		"app",
		corev1.SchemeGroupVersion.WithResource("pods"),
		"",
		op,
		nil,
		false,
		&user.DefaultInfo{Name: "admin"},
	)
}

func eventCount(t *testing.T, p *plugin) int {
	t.Helper()
	events, err := p.client.CoreV1().Events("default").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return len(events.Items)
}
