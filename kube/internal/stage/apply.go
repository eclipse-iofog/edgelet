package stage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/eclipse-iofog/edgelet-kube/internal/config"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
)

const fieldManager = "edgelet-kube"

// Apply writes the built-in addon manifests to the API server, then the
// RuntimeClass objects for handlers this node registers, then the HelmChart
// CRD, then the HelmChart objects, then extra. extra is additional YAML
// supplied by the caller. A disabled addon is deleted. A catalog handler
// whose binary is absent has its RuntimeClass deleted. Disabling the
// metrics server does not change API aggregation. The HelmChart CRD and the
// HelmChart objects are omitted when the Helm controller is disabled.
func Apply(ctx context.Context, kubeconfig string, srv config.Server, extra []byte) error {
	if kubeconfig == "" {
		return errors.New("kubeconfig is empty")
	}
	manifests, err := addonManifests(srv)
	if err != nil {
		return err
	}
	runtimes, err := runtimeManifests(srv)
	if err != nil {
		return err
	}
	manifests = append(manifests, runtimes...)
	crd, err := HelmChartCRD(srv)
	if err != nil {
		return err
	}
	charts, err := helmChartManifests(srv)
	if err != nil {
		return err
	}
	client, err := newApplier(kubeconfig)
	if err != nil {
		return err
	}
	deadline := time.NewTimer(2 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var last error
	for {
		last = client.syncAll(ctx, manifests, crd, charts, extra)
		if last == nil {
			return nil
		}
		if isFatal(last) {
			return last
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("addons were not applied: %w", ctx.Err())
		case <-deadline.C:
			return fmt.Errorf("addons were not applied: %w", last)
		case <-ticker.C:
		}
	}
}

type fatalError struct {
	err error
}

func (e fatalError) Error() string {
	return e.err.Error()
}

func (e fatalError) Unwrap() error {
	return e.err
}

func isFatal(err error) bool {
	var fatal fatalError
	return errors.As(err, &fatal)
}

type applier struct {
	dynamic dynamic.Interface
	cache   discovery.CachedDiscoveryInterface
	mapper  *restmapper.DeferredDiscoveryRESTMapper
}

func newApplier(kubeconfig string) (*applier, error) {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, err
	}
	cfg.QPS = 50
	cfg.Burst = 100
	cfg.UserAgent = fieldManager
	disco, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, err
	}
	cache := memory.NewMemCacheClient(disco)
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &applier{
		dynamic: dyn,
		cache:   cache,
		mapper:  restmapper.NewDeferredDiscoveryRESTMapper(cache),
	}, nil
}

func (a *applier) syncAll(ctx context.Context, manifests []manifest, crd []byte, charts []manifest, extra []byte) error {
	a.cache.Invalidate()
	a.mapper.Reset()
	for _, doc := range manifests {
		if doc.enabled {
			continue
		}
		if err := a.sync(ctx, doc.body, false); err != nil {
			return err
		}
	}
	for _, doc := range charts {
		if doc.enabled {
			continue
		}
		if err := a.sync(ctx, doc.body, false); err != nil {
			return err
		}
	}
	for _, body := range enabledBodies(manifests, crd, charts, extra) {
		if err := a.sync(ctx, body, true); err != nil {
			return err
		}
	}
	return nil
}

func enabledBodies(manifests []manifest, crd []byte, charts []manifest, extra []byte) [][]byte {
	out := make([][]byte, 0, len(manifests)+len(charts)+2)
	for _, doc := range manifests {
		if doc.enabled {
			out = append(out, doc.body)
		}
	}
	if len(bytes.TrimSpace(crd)) > 0 {
		out = append(out, crd)
	}
	for _, doc := range charts {
		if doc.enabled {
			out = append(out, doc.body)
		}
	}
	if len(bytes.TrimSpace(extra)) > 0 {
		out = append(out, extra)
	}
	return out
}

// HelmChartCRD is the embedded helm.cattle.io/v1 chart API.
// It is empty when the Helm controller is disabled.
func HelmChartCRD(srv config.Server) ([]byte, error) {
	if srv.DisableHelmController {
		return nil, nil
	}
	body, err := files.ReadFile("helmchart-crd.yaml")
	if err != nil {
		return nil, fmt.Errorf("read helm chart API: %w", err)
	}
	return body, nil
}

func (a *applier) sync(ctx context.Context, body []byte, enabled bool) error {
	objs, err := decode(body)
	if err != nil {
		return fatalError{err: err}
	}
	for _, obj := range objs {
		if !enabled && obj.GetKind() == "Namespace" {
			continue
		}
		var syncErr error
		if enabled {
			syncErr = a.apply(ctx, obj)
		} else {
			syncErr = a.remove(ctx, obj)
		}
		if syncErr != nil {
			return syncErr
		}
		if enabled && obj.GetKind() == "CustomResourceDefinition" {
			a.cache.Invalidate()
			a.mapper.Reset()
		}
	}
	return nil
}

func (a *applier) apply(ctx context.Context, obj *unstructured.Unstructured) error {
	obj = obj.DeepCopy()
	obj.SetManagedFields(nil)
	obj.SetResourceVersion("")
	obj.SetUID("")
	ri, err := a.resource(obj)
	if err != nil {
		return classify(fmt.Errorf("apply %s: %w", objectID(obj), err))
	}
	_, err = ri.Apply(ctx, obj.GetName(), obj, metav1.ApplyOptions{FieldManager: fieldManager, Force: true})
	if err != nil {
		return classify(fmt.Errorf("apply %s: %w", objectID(obj), err))
	}
	return nil
}

func (a *applier) remove(ctx context.Context, obj *unstructured.Unstructured) error {
	ri, err := a.resource(obj)
	if meta.IsNoMatchError(err) {
		return nil
	}
	if err != nil {
		return classify(fmt.Errorf("delete %s: %w", objectID(obj), err))
	}
	err = ri.Delete(ctx, obj.GetName(), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return classify(fmt.Errorf("delete %s: %w", objectID(obj), err))
	}
	return waitDeleted(ctx, ri, obj.GetName())
}

func waitDeleted(ctx context.Context, ri dynamic.ResourceInterface, name string) error {
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, err := ri.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			if err != nil {
				return fmt.Errorf("delete %s: %w", name, err)
			}
			return fmt.Errorf("delete %s timed out", name)
		case <-ticker.C:
		}
	}
}

func (a *applier) resource(obj *unstructured.Unstructured) (dynamic.ResourceInterface, error) {
	gvk := obj.GroupVersionKind()
	mapping, err := a.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if meta.IsNoMatchError(err) {
		a.cache.Invalidate()
		a.mapper.Reset()
		mapping, err = a.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	}
	if err != nil {
		return nil, err
	}
	ri := a.dynamic.Resource(mapping.Resource)
	if mapping.Scope.Name() != meta.RESTScopeNameNamespace {
		return ri, nil
	}
	ns := obj.GetNamespace()
	if ns == "" {
		return nil, fatalError{err: fmt.Errorf("%s has no namespace", objectID(obj))}
	}
	return ri.Namespace(ns), nil
}

func classify(err error) error {
	if apierrors.IsInvalid(err) || apierrors.IsBadRequest(err) || apierrors.IsUnauthorized(err) || apierrors.IsMethodNotSupported(err) {
		return fatalError{err: err}
	}
	return err
}

func objectID(obj *unstructured.Unstructured) string {
	if ns := obj.GetNamespace(); ns != "" {
		return obj.GetKind() + " " + ns + "/" + obj.GetName()
	}
	return obj.GetKind() + " " + obj.GetName()
}

func decode(body []byte) ([]*unstructured.Unstructured, error) {
	dec := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(body), 4096)
	var out []*unstructured.Unstructured
	for {
		obj := &unstructured.Unstructured{}
		err := dec.Decode(obj)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("decode manifest: %w", err)
		}
		if len(obj.Object) == 0 || obj.GetKind() == "" || obj.GetName() == "" {
			continue
		}
		out = append(out, obj)
	}
}
