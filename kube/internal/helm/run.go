package helm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet-kube/internal/config"
	"github.com/eclipse-iofog/edgelet-kube/scripts/airgap"
	helmapp "github.com/k3s-io/helm-controller/pkg/app"
	helmconfig "github.com/k3s-io/helm-controller/pkg/config"
	helmchart "github.com/k3s-io/helm-controller/pkg/controllers/chart"
	helmapi "github.com/k3s-io/helm-controller/pkg/generated/controllers/helm.cattle.io"
	"github.com/rancher/lasso/pkg/cache"
	"github.com/rancher/lasso/pkg/client"
	"github.com/rancher/lasso/pkg/controller"
	"github.com/rancher/wrangler/v3/pkg/apply"
	batchfactory "github.com/rancher/wrangler/v3/pkg/generated/controllers/batch"
	corev1factory "github.com/rancher/wrangler/v3/pkg/generated/controllers/core"
	rbacfactory "github.com/rancher/wrangler/v3/pkg/generated/controllers/rbac"
	"github.com/rancher/wrangler/v3/pkg/generic"
	"github.com/rancher/wrangler/v3/pkg/ratelimit"
	"github.com/rancher/wrangler/v3/pkg/schemes"
	"github.com/rancher/wrangler/v3/pkg/start"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	typedv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

const (
	controllerName = "helm-controller"
	// LeaseName is the leader lock in kube-system.
	LeaseName = "helm-controller"
)

// startLeading watches HelmChart objects. It is not called until that API is served.
var startLeading = lead

// Run starts the Helm controller. It returns before any watch when the
// HelmChart API is not served.
func Run(ctx context.Context, kubeconfig string, srv config.Server) error {
	if err := waitChartAPI(ctx, kubeconfig); err != nil {
		return err
	}
	return startLeading(ctx, kubeconfig, srv)
}

func waitChartAPI(ctx context.Context, kubeconfig string) error {
	deadline := time.NewTimer(time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var last error
	for {
		last = chartAPIServed(ctx, kubeconfig)
		if last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("helm chart API is not served: %w", last)
		case <-deadline.C:
			return fmt.Errorf("helm chart API is not served: %w", last)
		case <-ticker.C:
		}
	}
}

func chartAPIServed(ctx context.Context, kubeconfig string) error {
	if kubeconfig == "" {
		return errors.New("kubeconfig is empty")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return err
	}
	cfg.Timeout = 5 * time.Second
	disco, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return err
	}
	list, err := disco.ServerResourcesForGroupVersion("helm.cattle.io/v1")
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, resource := range list.APIResources {
		have[resource.Name] = true
	}
	if !have["helmcharts"] || !have["helmchartconfigs"] {
		return errors.New("helm chart API is not served")
	}
	return nil
}

func lead(ctx context.Context, kubeconfig string, srv config.Server) error {
	opts, port, err := controllerOptions(srv)
	if err != nil {
		return err
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig},
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return err
	}
	cfg.RateLimiter = ratelimit.None
	cfg.UserAgent = controllerName
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	if err := ensureSystemNamespace(ctx, kube); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	watching := make(chan error, 1)
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				watching <- fmt.Errorf("helm controller: %v", rec)
			}
		}()
		leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
			Lock:            leaseLock(kube),
			LeaseDuration:   45 * time.Second,
			RenewDeadline:   30 * time.Second,
			RetryPeriod:     2 * time.Second,
			ReleaseOnCancel: true,
			Callbacks: leaderelection.LeaderCallbacks{
				OnStartedLeading: func(ctx context.Context) {
					watching <- runControllers(ctx, cfg, opts, port)
				},
				OnStoppedLeading: func() {},
			},
		})
	}()
	select {
	case <-ctx.Done():
		return nil
	case err := <-watching:
		if err != nil {
			return err
		}
		<-ctx.Done()
		return nil
	}
}

func leaseLock(kube kubernetes.Interface) resourcelock.Interface {
	lock, err := resourcelock.New(
		resourcelock.LeasesResourceLock,
		metav1.NamespaceSystem,
		LeaseName,
		kube.CoreV1(),
		kube.CoordinationV1(),
		resourcelock.ResourceLockConfig{Identity: leaseIdentity()},
	)
	if err != nil {
		panic(err)
	}
	return lock
}

func leaseIdentity() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return controllerName
	}
	return name
}

func ensureSystemNamespace(ctx context.Context, kube kubernetes.Interface) error {
	_, err := kube.CoreV1().Namespaces().Get(ctx, metav1.NamespaceSystem, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	_, err = kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: metav1.NamespaceSystem},
	}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func runControllers(ctx context.Context, cfg *rest.Config, opts *helmconfig.Controller, port string) error {
	helmchart.DefaultJobImage = opts.DefaultJobImage
	helmchart.JobResources = opts.JobResources
	helmchart.JobTolerations = opts.JobTolerations

	apply, err := apply.NewForConfig(cfg)
	if err != nil {
		return err
	}
	apply = apply.WithSetOwnerReference(false, false).WithDynamicLookup().WithContext(ctx)

	scf, err := sharedFactory(cfg)
	if err != nil {
		return err
	}
	coreFactory, err := corev1factory.NewFactoryFromConfigWithOptions(cfg, &generic.FactoryOptions{
		SharedControllerFactory: scf,
	})
	if err != nil {
		return err
	}
	batchFactory, err := batchfactory.NewFactoryFromConfigWithOptions(cfg, &generic.FactoryOptions{
		SharedControllerFactory: scf,
	})
	if err != nil {
		return err
	}
	rbacFactory, err := rbacfactory.NewFactoryFromConfigWithOptions(cfg, &generic.FactoryOptions{
		SharedControllerFactory: scf,
	})
	if err != nil {
		return err
	}
	helmFactory, err := helmapi.NewFactoryFromConfigWithOptions(cfg, &generic.FactoryOptions{
		SharedControllerFactory: scf,
	})
	if err != nil {
		return err
	}

	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	broadcaster := record.NewBroadcaster(record.WithContext(ctx))
	go func() {
		<-ctx.Done()
		broadcaster.Shutdown()
	}()
	broadcaster.StartStructuredLogging(0)
	broadcaster.StartRecordingToSink(&typedv1.EventSinkImpl{Interface: kube.CoreV1().Events("")})
	recorder := broadcaster.NewRecorder(schemes.All, corev1.EventSource{
		Component: controllerName,
		Host:      opts.NodeName,
	})

	helmchart.Register(
		ctx,
		"",
		controllerName,
		opts.JobClusterRole,
		port,
		apply,
		recorder,
		batchFactory.Batch().V1(),
		coreFactory.Core().V1(),
		helmFactory.Helm().V1(),
		rbacFactory.Rbac().V1(),
	)
	klog.FromContext(ctx).Info("starting helm controller", "jobImage", helmchart.DefaultJobImage, "port", port)
	return start.All(ctx, 50, coreFactory, batchFactory, rbacFactory, helmFactory)
}

func sharedFactory(cfg *rest.Config) (controller.SharedControllerFactory, error) {
	rateLimit := workqueue.NewItemExponentialFailureRateLimiter(5*time.Millisecond, 60*time.Second)
	clientFactory, err := client.NewSharedClientFactory(cfg, nil)
	if err != nil {
		return nil, err
	}
	cacheFactory := cache.NewSharedCachedFactory(clientFactory, nil)
	return controller.NewSharedControllerFactory(cacheFactory, &controller.SharedControllerFactoryOptions{
		DefaultRateLimiter: rateLimit,
		DefaultWorkers:     50,
	}), nil
}

func controllerOptions(srv config.Server) (*helmconfig.Controller, string, error) {
	image, err := jobImage(srv.SystemDefaultRegistry)
	if err != nil {
		return nil, "", err
	}
	if srv.HTTPSListenPort == 0 {
		return nil, "", errors.New("API port is not configured")
	}
	args := []string{
		"--threads=2",
		"--job-cluster-role=cluster-admin",
		"--default-job-image=" + image,
		`--job-resources={"requests":{"cpu":"0.1","memory":"10M"},"limits":{"cpu":"32","memory":"32G"}}`,
	}
	args = append(args, flagArgs(srv.HelmControllerArg)...)
	opts, err := helmapp.Config(args)
	if err != nil {
		return nil, "", err
	}
	if opts.DefaultJobImage == "" {
		opts.DefaultJobImage = image
	}
	if opts.NodeName == "" {
		opts.NodeName = leaseIdentity()
	}
	return opts, strconv.Itoa(srv.HTTPSListenPort), nil
}

func jobImage(registry string) (string, error) {
	var match string
	for _, ref := range airgap.Images() {
		name, _, _ := strings.Cut(ref, "@")
		repo, _, ok := strings.Cut(name, ":")
		if !ok {
			continue
		}
		if repo != "docker.io/rancher/klipper-helm" && !strings.HasSuffix(repo, "/rancher/klipper-helm") {
			continue
		}
		if match != "" && match != ref {
			return "", errors.New("more than one pinned helm job image")
		}
		match = ref
	}
	if match == "" {
		return "", errors.New("pinned helm job image is not on the list")
	}
	return airgap.Resolve(registry, match), nil
}

func flagArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			arg = "--" + arg
		}
		out = append(out, arg)
	}
	return out
}
