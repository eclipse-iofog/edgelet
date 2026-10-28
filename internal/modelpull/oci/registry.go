package oci

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/registrytls"
	"github.com/eclipse-iofog/edgelet/pkg/imageref"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

// registryEndpoint is the HTTP identity of an OCI registry.
type registryEndpoint struct {
	Host      string
	Scheme    string
	PlainHTTP bool
	RepoRef   string // host/repo for oras NewRepository
	// AuthHost is the registry row host. Credentials are sent only to this host.
	AuthHost string
	// ApplyRegistryConfig is false when spec.repo names a different host.
	// That pull uses default HTTPS and does not take this row's credentials, CA, or plain HTTP.
	ApplyRegistryConfig bool
}

func parseRegistryEndpoint(reg *models.Registry, repo string) (registryEndpoint, error) {
	if reg == nil {
		return registryEndpoint{}, errors.New("registry is required")
	}
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return registryEndpoint{}, errors.New("repo is required")
	}
	if strings.Contains(repo, "://") || strings.HasPrefix(repo, "/") {
		return registryEndpoint{}, errors.New("spec.repo must not include a URL scheme")
	}
	if strings.TrimSpace(reg.URL) == "" {
		return registryEndpoint{}, errors.New("registry url is required")
	}
	ep, err := registrytls.Parse(reg)
	if err != nil {
		return registryEndpoint{}, err
	}
	path := strings.TrimPrefix(repo, "/")
	targetHost := ep.Host
	applyRegistry := true
	if imageref.HasRegistryHost(path) {
		refHost, rest, found := strings.Cut(path, "/")
		rest = strings.TrimPrefix(rest, "/")
		if !found || strings.TrimSpace(rest) == "" {
			return registryEndpoint{}, errors.New("repo is required")
		}
		if sameRegistryHost(refHost, ep.Host) {
			path = rest
		} else {
			path = rest
			targetHost = refHost
			applyRegistry = false
		}
	}
	scheme := ep.Scheme
	plainHTTP := ep.PlainHTTP
	if !applyRegistry {
		scheme = "https"
		plainHTTP = false
	}
	return registryEndpoint{
		Host:                targetHost,
		Scheme:              scheme,
		PlainHTTP:           plainHTTP,
		RepoRef:             targetHost + "/" + path,
		AuthHost:            ep.Host,
		ApplyRegistryConfig: applyRegistry,
	}, nil
}

func sameRegistryHost(a, b string) bool {
	left := imageref.SanitizeRegistryHost(a)
	right := imageref.SanitizeRegistryHost(b)
	return left != "" && strings.EqualFold(left, right)
}

func httpClientForRegistry(reg *models.Registry) (*http.Client, error) {
	transport, err := registrytls.Transport(reg)
	if err != nil {
		return nil, err
	}
	base := retry.DefaultClient
	client := *base
	client.Transport = retry.NewTransport(transport)
	return &client, nil
}

func newRemoteRepository(reg *models.Registry, repo string, httpClient *http.Client) (*remote.Repository, *auth.Client, registryEndpoint, error) {
	ep, err := parseRegistryEndpoint(reg, repo)
	if err != nil {
		return nil, nil, registryEndpoint{}, err
	}
	if httpClient == nil {
		if ep.ApplyRegistryConfig {
			httpClient, err = httpClientForRegistry(reg)
			if err != nil {
				return nil, nil, registryEndpoint{}, err
			}
		} else {
			base := retry.DefaultClient
			copied := *base
			httpClient = &copied
		}
	}
	authClient := &auth.Client{
		Client: httpClient,
		Cache:  auth.NewCache(),
		Header: http.Header{
			"User-Agent": {"edgelet-model-pull"},
		},
	}
	if ep.ApplyRegistryConfig && !reg.IsPublic && (strings.TrimSpace(reg.UserName) != "" || strings.TrimSpace(reg.Password) != "") {
		authClient.Credential = auth.StaticCredential(ep.AuthHost, auth.Credential{
			Username: reg.UserName,
			Password: reg.Password,
		})
	}
	remoteRepo, err := remote.NewRepository(ep.RepoRef)
	if err != nil {
		return nil, nil, registryEndpoint{}, fmt.Errorf("open registry repository: %w", err)
	}
	remoteRepo.Client = authClient
	remoteRepo.PlainHTTP = ep.PlainHTTP
	return remoteRepo, authClient, ep, nil
}
