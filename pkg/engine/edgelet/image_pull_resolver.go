//go:build linux

package edgelet

import (
	"strings"

	dockerresolver "github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/registrytls"
	"github.com/eclipse-iofog/edgelet/pkg/imageref"
)

func imagePullResolverOptions(registry *models.Registry) (dockerresolver.ResolverOptions, bool, error) {
	if registry == nil {
		return dockerresolver.ResolverOptions{}, false, nil
	}
	ep, err := registrytls.Parse(registry)
	if err != nil {
		return dockerresolver.ResolverOptions{}, false, err
	}
	needResolver := !registry.IsPublic || registry.Insecure || strings.TrimSpace(registry.CAB64) != "" || ep.PlainHTTP
	if !needResolver {
		return dockerresolver.ResolverOptions{}, false, nil
	}
	httpClient, err := registrytls.HTTPClient(registry)
	if err != nil {
		return dockerresolver.ResolverOptions{}, false, err
	}

	hostOpts := []dockerresolver.RegistryOpt{
		dockerresolver.WithClient(httpClient),
	}
	if ep.PlainHTTP {
		expected := ep.Host
		hostOpts = append(hostOpts, dockerresolver.WithPlainHTTP(func(host string) (bool, error) {
			return imageref.SanitizeRegistryHost(host) == expected, nil
		}))
	} else {
		hostOpts = append(hostOpts, dockerresolver.WithPlainHTTP(dockerresolver.MatchLocalhost))
	}
	if creds := imagePullCredentials(registry); creds != nil {
		hostOpts = append(hostOpts, dockerresolver.WithAuthorizer(dockerresolver.NewDockerAuthorizer(
			dockerresolver.WithAuthClient(httpClient),
			dockerresolver.WithAuthCreds(creds),
		)))
	}

	return dockerresolver.ResolverOptions{
		Hosts: dockerresolver.ConfigureDefaultRegistries(hostOpts...),
	}, true, nil
}

func imagePullCredentials(registry *models.Registry) func(string) (string, string, error) {
	if registry == nil || registry.IsPublic {
		return nil
	}
	expectedHost := imageref.SanitizeRegistryHost(registry.URL)
	return func(host string) (string, string, error) {
		if expectedHost != "" && imageref.SanitizeRegistryHost(host) != expectedHost {
			return "", "", nil
		}
		return registry.UserName, registry.Password, nil
	}
}
