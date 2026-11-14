package control

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/eclipse-iofog/edgelet-kube/internal/charts"
	"github.com/spf13/pflag"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	cliflag "k8s.io/component-base/cli/flag"
	apiapp "k8s.io/kubernetes/cmd/kube-apiserver/app"
	apioptions "k8s.io/kubernetes/cmd/kube-apiserver/app/options"
	cmapp "k8s.io/kubernetes/cmd/kube-controller-manager/app"
	cmoptions "k8s.io/kubernetes/cmd/kube-controller-manager/app/options"
	proxyapp "k8s.io/kubernetes/cmd/kube-proxy/app"
	schedapp "k8s.io/kubernetes/cmd/kube-scheduler/app"
	schedoptions "k8s.io/kubernetes/cmd/kube-scheduler/app/options"
	kubeletapp "k8s.io/kubernetes/cmd/kubelet/app"
	kubeletoptions "k8s.io/kubernetes/cmd/kubelet/app/options"
)

var (
	startKubelet   = launchKubelet
	startKubeProxy = launchKubeProxy
)

func runAPIServer(ctx context.Context, args []string) error {
	opts := apioptions.NewServerRunOptions()
	if err := parseFlags("kube-apiserver", opts.Flags(), args); err != nil {
		return err
	}
	if err := opts.GenericServerRunOptions.ComponentGlobalsRegistry.Set(); err != nil {
		return err
	}
	completed, err := opts.Complete(ctx)
	if err != nil {
		return err
	}
	if errs := completed.Validate(); len(errs) > 0 {
		return utilerrors.NewAggregate(errs)
	}
	cfg, err := apiapp.NewConfig(completed)
	if err != nil {
		return err
	}
	finished, err := cfg.Complete()
	if err != nil {
		return err
	}
	server, err := apiapp.CreateServerChain(finished)
	if err != nil {
		return err
	}
	prepared, err := server.PrepareRun()
	if err != nil {
		return err
	}
	handler := server.GenericAPIServer.Handler
	handler.FullHandlerChain = charts.Handler(handler.FullHandlerChain)
	return prepared.Run(ctx)
}

func runControllerManager(ctx context.Context, args []string) error {
	opts, err := cmoptions.NewKubeControllerManagerOptions()
	if err != nil {
		return err
	}
	known := cmapp.KnownControllers()
	disabled := cmapp.ControllersDisabledByDefault()
	aliases := cmapp.ControllerAliases()
	if err := parseFlags("kube-controller-manager", opts.Flags(known, disabled, aliases), args); err != nil {
		return err
	}
	if err := opts.ComponentGlobalsRegistry.Set(); err != nil {
		return err
	}
	cfg, err := opts.Config(ctx, known, disabled, aliases)
	if err != nil {
		return err
	}
	return cmapp.Run(ctx, cfg.Complete())
}

func runScheduler(ctx context.Context, args []string) error {
	opts := schedoptions.NewOptions()
	if err := parseFlags("kube-scheduler", *opts.Flags, args); err != nil {
		return err
	}
	completed, sched, err := schedapp.Setup(ctx, opts)
	if err != nil {
		return err
	}
	return schedapp.Run(ctx, completed, sched)
}

func launchKubelet(ctx context.Context, args []string) error {
	server, err := loadKubelet(args)
	if err != nil {
		return err
	}
	// Run dereferences this value before it will build one itself.
	deps, err := kubeletapp.UnsecuredDependencies(ctx, server, utilfeature.DefaultFeatureGate)
	if err != nil {
		return err
	}
	return kubeletapp.Run(ctx, server, deps, utilfeature.DefaultFeatureGate)
}

func launchKubeProxy(ctx context.Context, args []string) error {
	opts, err := loadKubeProxy(args)
	if err != nil {
		return err
	}
	return opts.Run(ctx)
}

func loadKubelet(args []string) (*kubeletoptions.KubeletServer, error) {
	flags := kubeletoptions.NewKubeletFlags()
	cfg, err := kubeletoptions.NewKubeletConfiguration()
	if err != nil {
		return nil, err
	}
	fs := pflag.NewFlagSet("kubelet", pflag.ContinueOnError)
	var buf bytes.Buffer
	fs.SetOutput(&buf)
	flags.AddFlags(fs)
	kubeletoptions.AddKubeletConfigFlags(fs, cfg)
	acceptRuntimeFlags(fs)
	if err := fs.Parse(args); err != nil {
		return nil, flagParseError("kubelet", err, &buf)
	}
	if extra := fs.Args(); len(extra) > 0 {
		return nil, fmt.Errorf("kubelet: unknown command %s", extra[0])
	}
	server := &kubeletoptions.KubeletServer{
		KubeletFlags:         *flags,
		KubeletConfiguration: *cfg,
	}
	if err := kubeletoptions.ValidateKubeletServer(server); err != nil {
		return nil, err
	}
	return server, nil
}

func loadKubeProxy(args []string) (*proxyapp.Options, error) {
	opts := proxyapp.NewOptions()
	fs := pflag.NewFlagSet("kube-proxy", pflag.ContinueOnError)
	var buf bytes.Buffer
	fs.SetOutput(&buf)
	opts.AddFlags(fs)
	if err := fs.Parse(args); err != nil {
		return nil, flagParseError("kube-proxy", err, &buf)
	}
	if err := opts.Complete(fs); err != nil {
		return nil, err
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	return opts, nil
}

// acceptRuntimeFlags keeps the sandbox image and CNI directory flags on the
// kubelet command line. This build leaves both settings to the container
// runtime, which is started with the same values.
func acceptRuntimeFlags(fs *pflag.FlagSet) {
	var confDir, binDir, pause string
	if fs.Lookup("cni-conf-dir") == nil {
		fs.StringVar(&confDir, "cni-conf-dir", "", "CNI configuration directory")
	}
	if fs.Lookup("cni-bin-dir") == nil {
		fs.StringVar(&binDir, "cni-bin-dir", "", "CNI plugin directory")
	}
	if fs.Lookup("pod-infra-container-image") == nil {
		fs.StringVar(&pause, "pod-infra-container-image", "", "sandbox image")
	}
}

func parseFlags(name string, sets cliflag.NamedFlagSets, args []string) error {
	fs := pflag.NewFlagSet(name, pflag.ContinueOnError)
	var buf bytes.Buffer
	fs.SetOutput(&buf)
	for _, set := range sets.FlagSets {
		fs.AddFlagSet(set)
	}
	if err := fs.Parse(args); err != nil {
		return flagParseError(name, err, &buf)
	}
	return nil
}

func flagParseError(name string, err error, buf io.Reader) error {
	detail, _ := io.ReadAll(buf)
	if len(detail) == 0 {
		return fmt.Errorf("%s: %w", name, err)
	}
	return fmt.Errorf("%s: %w: %s", name, err, bytes.TrimSpace(detail))
}

func report(errCh chan<- error, name string, err error) {
	if err == nil {
		return
	}
	select {
	case errCh <- fmt.Errorf("%s: %w", name, err):
	default:
	}
}
