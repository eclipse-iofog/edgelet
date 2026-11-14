package stage

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/eclipse-iofog/edgelet-kube/internal/charts"
	"github.com/eclipse-iofog/edgelet-kube/internal/config"
	"github.com/eclipse-iofog/edgelet-kube/scripts/airgap"
	"go.yaml.in/yaml/v3"
)

// chartOrigin is the in-cluster address of this apiserver.
const chartOrigin = "https://kubernetes.default.svc"

type chartObject struct {
	APIVersion string     `yaml:"apiVersion"`
	Kind       string     `yaml:"kind"`
	Metadata   objectMeta `yaml:"metadata"`
	Spec       chartSpec  `yaml:"spec"`
}

type objectMeta struct {
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace"`
}

type chartSpec struct {
	Chart           string `yaml:"chart"`
	TargetNamespace string `yaml:"targetNamespace,omitempty"`
	CreateNamespace bool   `yaml:"createNamespace,omitempty"`
	FailurePolicy   string `yaml:"failurePolicy,omitempty"`
	ForceConflicts  bool   `yaml:"forceConflicts,omitempty"`
	ValuesContent   string `yaml:"valuesContent,omitempty"`
}

// helmChartManifests renders the Traefik, Gateway API CRD, and cert-manager releases.
// The chart URLs point at this apiserver. Nothing is returned when the Helm
// controller is disabled. The Gateway API chart installs CRDs only. Traefik
// stays an Ingress controller.
func helmChartManifests(srv config.Server) ([]manifest, error) {
	if srv.DisableHelmController {
		return nil, nil
	}
	names, err := charts.Names()
	if err != nil {
		return nil, err
	}
	traefikCRD, err := chartDocument(names, "traefik-crd", "", false, nil)
	if err != nil {
		return nil, err
	}
	traefikValues, err := traefikValues(srv)
	if err != nil {
		return nil, err
	}
	traefik, err := chartDocument(names, "traefik", "", false, traefikValues)
	if err != nil {
		return nil, err
	}
	gateway, err := chartDocument(names, "gateway-api-crd", "", false, nil)
	if err != nil {
		return nil, err
	}
	certValues, err := certManagerValues(srv)
	if err != nil {
		return nil, err
	}
	cert, err := chartDocument(names, "cert-manager", "cert-manager", true, certValues)
	if err != nil {
		return nil, err
	}
	return []manifest{
		{
			name:    "traefik",
			enabled: !disabled(srv.Disable, "traefik"),
			body:    joinYAML(traefikCRD, traefik),
		},
		{
			name:    "gateway-api-crd",
			enabled: !disabled(srv.Disable, "gateway-api-crd"),
			body:    gateway,
		},
		{
			name:    "cert-manager",
			enabled: !addonOff(srv.Disable, "cert-manager"),
			body:    cert,
		},
	}, nil
}

func chartDocument(names []string, chart, namespace string, createNamespace bool, values map[string]any) ([]byte, error) {
	file, err := chartFile(names, chart)
	if err != nil {
		return nil, err
	}
	spec := chartSpec{
		Chart:          chartOrigin + charts.Prefix + file,
		FailurePolicy:  "retry",
		ForceConflicts: true,
	}
	if namespace != "" {
		spec.TargetNamespace = namespace
		spec.CreateNamespace = createNamespace
	}
	if values != nil {
		raw, err := yaml.Marshal(values)
		if err != nil {
			return nil, fmt.Errorf("chart %s values: %w", chart, err)
		}
		spec.ValuesContent = string(raw)
	}
	body, err := yaml.Marshal(chartObject{
		APIVersion: "helm.cattle.io/v1",
		Kind:       "HelmChart",
		Metadata:   objectMeta{Name: chart, Namespace: "kube-system"},
		Spec:       spec,
	})
	if err != nil {
		return nil, fmt.Errorf("chart %s: %w", chart, err)
	}
	return body, nil
}

func chartFile(names []string, chart string) (string, error) {
	version := airgap.ChartVersion(chart)
	if version == "" {
		return "", fmt.Errorf("chart %s has no pinned version", chart)
	}
	want := chart + "-" + version + ".tgz"
	for _, name := range names {
		if name == want {
			return name, nil
		}
	}
	return "", fmt.Errorf("chart %s is not embedded", chart)
}

func traefikValues(srv config.Server) (map[string]any, error) {
	repository, tag, digest, err := chartImage(srv.SystemDefaultRegistry, "library/traefik")
	if err != nil {
		return nil, err
	}
	registry, name, ok := strings.Cut(repository, "/")
	if !ok || registry == "" || name == "" {
		return nil, errors.New("pinned image library/traefik has no registry")
	}
	return map[string]any{
		"image": map[string]any{
			"registry":   registry,
			"repository": name,
			"tag":        tag,
			"digest":     digest,
		},
		"ports": map[string]any{
			"web":       map[string]any{"exposedPort": 80},
			"websecure": map[string]any{"exposedPort": 443},
		},
		"service": map[string]any{
			"spec": map[string]any{"type": "LoadBalancer"},
		},
	}, nil
}

func certManagerValues(srv config.Server) (map[string]any, error) {
	controller, err := imageValue(srv.SystemDefaultRegistry, "cert-manager-controller")
	if err != nil {
		return nil, err
	}
	webhook, err := imageValue(srv.SystemDefaultRegistry, "cert-manager-webhook")
	if err != nil {
		return nil, err
	}
	cainjector, err := imageValue(srv.SystemDefaultRegistry, "cert-manager-cainjector")
	if err != nil {
		return nil, err
	}
	startup, err := imageValue(srv.SystemDefaultRegistry, "cert-manager-startupapicheck")
	if err != nil {
		return nil, err
	}
	solver, err := imageValue(srv.SystemDefaultRegistry, "cert-manager-acmesolver")
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"acmesolver":      map[string]any{"image": solver},
		"cainjector":      map[string]any{"image": cainjector},
		"crds":            map[string]any{"enabled": true},
		"image":           controller,
		"startupapicheck": map[string]any{"image": startup},
		"webhook":         map[string]any{"image": webhook},
	}, nil
}

func imageValue(registry, repo string) (map[string]any, error) {
	repository, tag, digest, err := chartImage(registry, repo)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"digest":     digest,
		"repository": repository,
		"tag":        tag,
	}, nil
}

// chartImage splits a pinned reference into repository, tag, and digest.
// repository includes the registry. An optional system registry is already applied.
func chartImage(registry, repo string) (string, string, string, error) {
	ref, err := imageRef(registry, repo)
	if err != nil {
		return "", "", "", err
	}
	name, digest, ok := strings.Cut(ref, "@")
	if !ok || !strings.HasPrefix(digest, "sha256:") {
		return "", "", "", fmt.Errorf("pinned image %s has no digest", repo)
	}
	slash := strings.LastIndex(name, "/")
	colon := strings.LastIndex(name, ":")
	if colon <= slash {
		return "", "", "", fmt.Errorf("pinned image %s has no tag", repo)
	}
	return name[:colon], name[colon+1:], digest, nil
}

func joinYAML(docs ...[]byte) []byte {
	var buf bytes.Buffer
	for i, doc := range docs {
		if i > 0 {
			buf.WriteString("---\n")
		}
		buf.Write(bytes.TrimSpace(doc))
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}
