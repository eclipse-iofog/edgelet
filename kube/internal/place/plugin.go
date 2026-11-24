package place

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/initializer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/api/legacyscheme"
	core "k8s.io/kubernetes/pkg/apis/core"
	_ "k8s.io/kubernetes/pkg/apis/core/install" // register pod conversion
)

// PluginName is the admission plugin that assigns pods to this node.
const PluginName = "NodePlace"

type plugin struct {
	*admission.Handler
	nodeName string
	client   kubernetes.Interface
	nodes    corelisters.NodeLister
	pods     corelisters.PodLister
}

var (
	_ admission.MutationInterface                  = &plugin{}
	_ initializer.WantsExternalKubeClientSet       = &plugin{}
	_ initializer.WantsExternalKubeInformerFactory = &plugin{}
	_ admission.InitializationValidator            = &plugin{}
)

// Register adds PluginName to the apiserver admission set.
// order is the recommended plugin order; the returned order includes PluginName.
func Register(plugins *admission.Plugins, order []string, nodeName string) []string {
	plugins.Register(PluginName, func(_ io.Reader) (admission.Interface, error) {
		return newPlugin(nodeName), nil
	})
	if slices.Contains(order, PluginName) {
		return order
	}
	return append(order, PluginName)
}

func newPlugin(nodeName string) *plugin {
	return &plugin{
		Handler:  admission.NewHandler(admission.Create),
		nodeName: nodeName,
	}
}

func (p *plugin) SetExternalKubeClientSet(client kubernetes.Interface) {
	p.client = client
}

func (p *plugin) SetExternalKubeInformerFactory(factory informers.SharedInformerFactory) {
	nodes := factory.Core().V1().Nodes()
	pods := factory.Core().V1().Pods()
	p.nodes = nodes.Lister()
	p.pods = pods.Lister()
	p.SetReadyFunc(func() bool {
		return nodes.Informer().HasSynced() && pods.Informer().HasSynced()
	})
}

func (p *plugin) ValidateInitialization() error {
	if p.nodeName == "" {
		return errors.New("node name is empty")
	}
	if p.client == nil || p.nodes == nil || p.pods == nil {
		return errors.New("placement client is not initialized")
	}
	return nil
}

func (p *plugin) Admit(ctx context.Context, a admission.Attributes, o admission.ObjectInterfaces) error {
	if a.GetOperation() != admission.Create || a.GetSubresource() != "" {
		return nil
	}
	if a.GetResource().GroupResource() != corev1.Resource("pods") {
		return nil
	}
	obj := a.GetObject()
	current, ok := nodeNameOf(obj)
	if !ok || current != "" {
		return nil
	}
	if !p.WaitForReady() {
		return nil
	}
	pod, err := externalPod(obj, objectConvertor(o))
	if err != nil {
		return err
	}
	pod = pod.DeepCopy()
	if pod.Namespace == "" {
		pod.Namespace = a.GetNamespace()
	}
	if pod.Name == "" {
		pod.Name = a.GetName()
	}
	node, scheduled, err := p.snapshot()
	if err != nil {
		klog.ErrorS(err, "read placement state", "pod", pod.Namespace+"/"+pod.Name)
		return nil
	}
	decision := decide(p.nodeName, pod, node, scheduled)
	if decision.assign {
		if !a.IsDryRun() && p.client != nil {
			if err := markClaims(ctx, p.client, p.nodeName, pod); err != nil {
				klog.V(2).InfoS("defer pod placement", "pod", pod.Namespace+"/"+pod.Name, "err", err)
				return nil
			}
		}
		setNodeName(obj, p.nodeName)
		return nil
	}
	if decision.reason == "" || a.IsDryRun() || p.client == nil {
		return nil
	}
	p.record(ctx, pod, decision.reason, decision.message)
	return nil
}

func (p *plugin) snapshot() (*corev1.Node, []*corev1.Pod, error) {
	if p.nodes == nil || p.pods == nil {
		return nil, nil, errors.New("placement cache is not initialized")
	}
	node, err := p.nodes.Get(p.nodeName)
	if apierrors.IsNotFound(err) {
		node, err = nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	pods, err := p.pods.List(labels.Everything())
	if err != nil {
		return nil, nil, err
	}
	return node, pods, nil
}

func (p *plugin) record(ctx context.Context, pod *corev1.Pod, reason, message string) {
	now := metav1.Now()
	event := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      eventName(pod.Name, now.Time),
			Namespace: pod.Namespace,
		},
		InvolvedObject: corev1.ObjectReference{
			Kind:       "Pod",
			APIVersion: "v1",
			Namespace:  pod.Namespace,
			Name:       pod.Name,
			UID:        pod.UID,
		},
		Reason:              reason,
		Message:             message,
		Type:                corev1.EventTypeWarning,
		Source:              corev1.EventSource{Component: "nodeplace"},
		FirstTimestamp:      now,
		LastTimestamp:       now,
		Count:               1,
		EventTime:           metav1.NewMicroTime(now.Time),
		Action:              "Binding",
		ReportingController: "nodeplace",
		ReportingInstance:   p.nodeName,
	}
	if _, err := p.client.CoreV1().Events(pod.Namespace).Create(ctx, event, metav1.CreateOptions{}); err != nil {
		klog.ErrorS(err, "record placement event", "pod", pod.Namespace+"/"+pod.Name, "reason", reason)
	}
}

func eventName(podName string, now time.Time) string {
	suffix := fmt.Sprintf(".%x", now.UnixNano())
	if len(podName)+len(suffix) > 253 {
		podName = podName[:253-len(suffix)]
	}
	return podName + suffix
}

func nodeNameOf(obj runtime.Object) (string, bool) {
	switch pod := obj.(type) {
	case *corev1.Pod:
		return pod.Spec.NodeName, true
	case *core.Pod:
		return pod.Spec.NodeName, true
	default:
		return "", false
	}
}

func setNodeName(obj runtime.Object, name string) {
	switch pod := obj.(type) {
	case *corev1.Pod:
		pod.Spec.NodeName = name
	case *core.Pod:
		pod.Spec.NodeName = name
	}
}

func externalPod(obj runtime.Object, convertor runtime.ObjectConvertor) (*corev1.Pod, error) {
	if pod, ok := obj.(*corev1.Pod); ok {
		return pod, nil
	}
	if convertor == nil {
		convertor = legacyscheme.Scheme
	}
	out := &corev1.Pod{}
	if err := convertor.Convert(obj, out, nil); err != nil {
		return nil, fmt.Errorf("convert pod: %w", err)
	}
	return out, nil
}

func objectConvertor(o admission.ObjectInterfaces) runtime.ObjectConvertor {
	if o == nil {
		return nil
	}
	return o.GetObjectConvertor()
}
