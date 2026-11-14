package stage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet-kube/internal/config"
	"go.yaml.in/yaml/v3"
	"k8s.io/klog/v2"
)

// ApplyIssuer writes the selfsigned ClusterIssuer whenever cert-manager is
// staged. It also writes letsencrypt-prod when an email is configured and
// traefik is staged. It retries until cert-manager serves the ClusterIssuer
// CRD. An empty email writes no letsencrypt-prod. When the email is set and
// traefik or cert-manager is disabled, letsencrypt-prod is not written and
// one line is logged.
func ApplyIssuer(ctx context.Context, kubeconfig string, srv config.Server) error {
	body, skip, err := clusterIssuer(srv)
	if err != nil {
		return err
	}
	if skip {
		klog.Info("letsencrypt cluster issuer was skipped because traefik or cert-manager is disabled")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if kubeconfig == "" {
		return errors.New("kubeconfig is empty")
	}
	client, err := newApplier(kubeconfig)
	if err != nil {
		return err
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var last error
	for {
		client.cache.Invalidate()
		client.mapper.Reset()
		last = client.sync(ctx, body, true)
		if last == nil {
			return nil
		}
		if isFatal(last) {
			return last
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("cluster issuer was not applied: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func clusterIssuer(srv config.Server) ([]byte, bool, error) {
	email := strings.TrimSpace(srv.ClusterIssuerEmail)
	certOff := addonOff(srv.Disable, "cert-manager")
	traefikOff := disabled(srv.Disable, "traefik")
	skip := email != "" && (certOff || traefikOff)
	if srv.DisableHelmController || certOff {
		return nil, skip, nil
	}
	selfSigned, err := yaml.Marshal(selfSignedDoc())
	if err != nil {
		return nil, false, err
	}
	docs := [][]byte{selfSigned}
	if email != "" && !traefikOff {
		letsEncrypt, err := yaml.Marshal(issuerDoc(email))
		if err != nil {
			return nil, false, err
		}
		docs = append(docs, letsEncrypt)
	}
	return joinYAML(docs...), skip, nil
}

type selfSignedIssuer struct {
	APIVersion string         `yaml:"apiVersion"`
	Kind       string         `yaml:"kind"`
	Metadata   issuerMeta     `yaml:"metadata"`
	Spec       selfSignedSpec `yaml:"spec"`
}

type emptyObject struct{}

type selfSignedSpec struct {
	SelfSigned emptyObject `yaml:"selfSigned"`
}

func selfSignedDoc() selfSignedIssuer {
	return selfSignedIssuer{
		APIVersion: "cert-manager.io/v1",
		Kind:       "ClusterIssuer",
		Metadata:   issuerMeta{Name: "selfsigned"},
	}
}

type issuerObject struct {
	APIVersion string         `yaml:"apiVersion"`
	Kind       string         `yaml:"kind"`
	Metadata   issuerMeta     `yaml:"metadata"`
	Spec       issuerSpecBody `yaml:"spec"`
}

type issuerMeta struct {
	Name string `yaml:"name"`
}

type issuerSpecBody struct {
	ACME issuerACME `yaml:"acme"`
}

type issuerACME struct {
	Server              string             `yaml:"server"`
	Email               string             `yaml:"email"`
	PrivateKeySecretRef issuerSecretRef    `yaml:"privateKeySecretRef"`
	Solvers             []issuerHTTPSolver `yaml:"solvers"`
}

type issuerSecretRef struct {
	Name string `yaml:"name"`
}

type issuerHTTPSolver struct {
	HTTP01 issuerHTTP01 `yaml:"http01"`
}

type issuerHTTP01 struct {
	Ingress issuerIngress `yaml:"ingress"`
}

type issuerIngress struct {
	Class string `yaml:"class"`
}

func issuerDoc(email string) issuerObject {
	solver := issuerHTTPSolver{}
	solver.HTTP01.Ingress.Class = "traefik"
	return issuerObject{
		APIVersion: "cert-manager.io/v1",
		Kind:       "ClusterIssuer",
		Metadata:   issuerMeta{Name: "letsencrypt-prod"},
		Spec: issuerSpecBody{ACME: issuerACME{
			Server:              "https://acme-v02.api.letsencrypt.org/directory",
			Email:               email,
			PrivateKeySecretRef: issuerSecretRef{Name: "letsencrypt-prod-account-key"},
			Solvers:             []issuerHTTPSolver{solver},
		}},
	}
}
