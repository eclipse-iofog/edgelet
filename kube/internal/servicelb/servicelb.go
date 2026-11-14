// Package servicelb publishes the node IP on LoadBalancer Services.
// kube-proxy programs forwarding from that address. The controller does not
// create pods or DaemonSets.
package servicelb

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/workqueue"
)

// Run watches Services and writes address onto each LoadBalancer Service.
func Run(ctx context.Context, kubeconfig, address string) error {
	ip := net.ParseIP(address)
	if ip == nil {
		return fmt.Errorf("node IP %q is not an IP", address)
	}
	if kubeconfig == "" {
		return errors.New("kubeconfig is empty")
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
	return run(ctx, client, ip.String())
}

func run(ctx context.Context, client kubernetes.Interface, address string) error {
	factory := informers.NewSharedInformerFactory(client, 30*time.Second)
	services := factory.Core().V1().Services()
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())
	defer queue.ShutDown()
	c := &controller{
		client:  client,
		address: address,
		lister:  services.Lister(),
		queue:   queue,
	}
	if _, err := services.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { c.enqueue(obj) },
		UpdateFunc: func(_, obj any) { c.enqueue(obj) },
	}); err != nil {
		return err
	}
	factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), services.Informer().HasSynced) {
		if ctx.Err() != nil {
			return nil
		}
		return errors.New("services were not listed")
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
	client  kubernetes.Interface
	address string
	lister  corelisters.ServiceLister
	queue   workqueue.TypedRateLimitingInterface[string]
}

func (c *controller) enqueue(obj any) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		return
	}
	c.queue.Add(key)
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
	svc, err := c.lister.Services(namespace).Get(name)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if svc.DeletionTimestamp != nil {
		return nil
	}
	if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
		if !published(svc, c.address) {
			return nil
		}
		updated := svc.DeepCopy()
		updated.Status.LoadBalancer = corev1.LoadBalancerStatus{}
		_, err = c.client.CoreV1().Services(namespace).UpdateStatus(ctx, updated, metav1.UpdateOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if published(svc, c.address) {
		return nil
	}
	updated := svc.DeepCopy()
	updated.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: c.address}}
	_, err = c.client.CoreV1().Services(namespace).UpdateStatus(ctx, updated, metav1.UpdateOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func published(svc *corev1.Service, address string) bool {
	ingress := svc.Status.LoadBalancer.Ingress
	return len(ingress) == 1 && ingress[0].IP == address && ingress[0].Hostname == ""
}
