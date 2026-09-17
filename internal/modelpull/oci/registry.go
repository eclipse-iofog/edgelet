package oci

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/registrytls"
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
}

func parseRegistryEndpoint(reg *models.Registry, repo string) (registryEndpoint, error) {
	if reg == nil {
		return registryEndpoint{}, errors.New("registry is required")
	}
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return registryEndpoint{}, errors.New("repo is required")
	}
	if strings.TrimSpace(reg.URL) == "" {
		return registryEndpoint{}, errors.New("registry url is required")
	}
	ep, err := registrytls.Parse(reg)
	if err != nil {
		return registryEndpoint{}, err
	}
	return registryEndpoint{
		Host:      ep.Host,
		Scheme:    ep.Scheme,
		PlainHTTP: ep.PlainHTTP,
		RepoRef:   ep.Host + "/" + strings.TrimPrefix(repo, "/"),
	}, nil
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
		httpClient, err = httpClientForRegistry(reg)
		if err != nil {
			return nil, nil, registryEndpoint{}, err
		}
	}
	authClient := &auth.Client{
		Client: httpClient,
		Cache:  auth.NewCache(),
		Header: http.Header{
			"User-Agent": {"edgelet-model-pull"},
		},
	}
	if !reg.IsPublic && (strings.TrimSpace(reg.UserName) != "" || strings.TrimSpace(reg.Password) != "") {
		authClient.Credential = auth.StaticCredential(ep.Host, auth.Credential{
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
