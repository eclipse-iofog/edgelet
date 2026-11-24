package place

import (
	"context"
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/workqueue"
)

// Run watches unbound pods and binds them to nodeName once they can run here.
// The client is built from the admin kubeconfig. Binding uses the Pod Binding subresource.
func Run(ctx context.Context, kubeconfig, nodeName string) error {
	if kubeconfig == "" {
		return errors.New("kubeconfig is empty")
	}
	if nodeName == "" {
		return errors.New("node name is empty")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return err
	}
	cfg.UserAgent = "edgelet-kube"
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	return run(ctx, client, nodeName)
}

func run(ctx context.Context, client kubernetes.Interface, nodeName string) error {
	factory := informers.NewSharedInformerFactory(client, 15*time.Second)
	pods := factory.Core().V1().Pods()
	nodes := factory.Core().V1().Nodes()
	claims := factory.Core().V1().PersistentVolumeClaims()
	classes := factory.Storage().V1().StorageClasses()
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())
	defer queue.ShutDown()
	c := &controller{
		client:   client,
		nodeName: nodeName,
		pods:     pods.Lister(),
		nodes:    nodes.Lister(),
		queue:    queue,
	}
	if _, err := pods.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { c.onPod(nil, obj) },
		UpdateFunc: func(oldObj, obj any) { c.onPod(oldObj, obj) },
		DeleteFunc: func(obj any) { c.requeuePending() },
	}); err != nil {
		return err
	}
	requeue := cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { c.requeuePending() },
		UpdateFunc: func(any, any) { c.requeuePending() },
	}
	if _, err := nodes.Informer().AddEventHandler(requeue); err != nil {
		return err
	}
	if _, err := claims.Informer().AddEventHandler(requeue); err != nil {
		return err
	}
	if _, err := classes.Informer().AddEventHandler(requeue); err != nil {
		return err
	}
	factory.Start(ctx.Done())
	synced := []cache.InformerSynced{
		pods.Informer().HasSynced,
		nodes.Informer().HasSynced,
		claims.Informer().HasSynced,
		classes.Informer().HasSynced,
	}
	if !cache.WaitForCacheSync(ctx.Done(), synced...) {
		if ctx.Err() != nil {
			return nil
		}
		return errors.New("placement cache was not synced")
	}
	go func() {
		<-ctx.Done()
		queue.ShutDown()
	}()
	for c.processNext(ctx) {
	}
	return nil
}

type controller struct {
	client   kubernetes.Interface
	nodeName string
	pods     corelisters.PodLister
	nodes    corelisters.NodeLister
	queue    workqueue.TypedRateLimitingInterface[string]
}

func (c *controller) onPod(oldObj, obj any) {
	pod, ok := podFrom(obj)
	if !ok {
		return
	}
	if pod.Spec.NodeName == "" && schedulesHere(pod) {
		c.queue.Add(pod.Namespace + "/" + pod.Name)
		return
	}
	if oldObj == nil {
		return
	}
	previous, ok := podFrom(oldObj)
	if ok && occupies(previous) && !occupies(pod) {
		c.requeuePending()
	}
}

func podFrom(obj any) (*corev1.Pod, bool) {
	switch value := obj.(type) {
	case *corev1.Pod:
		return value, true
	case cache.DeletedFinalStateUnknown:
		pod, ok := value.Obj.(*corev1.Pod)
		return pod, ok
	default:
		return nil, false
	}
}

func (c *controller) requeuePending() {
	pods, err := c.pods.List(labels.Everything())
	if err != nil {
		return
	}
	for _, pod := range pods {
		if pod.Spec.NodeName == "" && schedulesHere(pod) {
			c.queue.Add(pod.Namespace + "/" + pod.Name)
		}
	}
}

func (c *controller) processNext(ctx context.Context) bool {
	key, shutdown := c.queue.Get()
	if shutdown {
		return false
	}
	defer c.queue.Done(key)
	err := c.sync(ctx, key)
	if err != nil && ctx.Err() == nil {
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	if ctx.Err() != nil {
		c.queue.ShutDown()
		return false
	}
	return true
}

func (c *controller) sync(ctx context.Context, key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return nil
	}
	pod, err := c.client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	node, scheduled, err := c.snapshot()
	if err != nil {
		return err
	}
	return assign(ctx, c.client, c.nodeName, node, scheduled, pod)
}

func (c *controller) snapshot() (*corev1.Node, []*corev1.Pod, error) {
	node, err := c.nodes.Get(c.nodeName)
	if apierrors.IsNotFound(err) {
		node, err = nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	pods, err := c.pods.List(labels.Everything())
	if err != nil {
		return nil, nil, err
	}
	return node, pods, nil
}

func assign(ctx context.Context, client kubernetes.Interface, nodeName string, node *corev1.Node, scheduled []*corev1.Pod, pod *corev1.Pod) error {
	if pod.Spec.NodeName != "" || pod.DeletionTimestamp != nil {
		return nil
	}
	decision := decide(nodeName, pod, node, scheduled)
	if !decision.assign {
		return nil
	}
	if err := markClaims(ctx, client, nodeName, pod); err != nil {
		return err
	}
	return bindPod(ctx, client, pod, nodeName)
}

func bindPod(ctx context.Context, client kubernetes.Interface, pod *corev1.Pod, nodeName string) error {
	binding := &corev1.Binding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pod.Name,
			Namespace: pod.Namespace,
			UID:       pod.UID,
		},
		Target: corev1.ObjectReference{
			Kind:       "Node",
			APIVersion: "v1",
			Name:       nodeName,
		},
	}
	err := client.CoreV1().Pods(pod.Namespace).Bind(ctx, binding, metav1.CreateOptions{})
	if apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}
