package registries

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/containerd/containerd/v2/core/remotes/docker"
	"go.yaml.in/yaml/v3"
)

// Auth is registry username and password, or a pre-encoded auth token.
type Auth struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	Token    string `yaml:"auth"`
}

type file struct {
	Mirrors map[string]mirror    `yaml:"mirrors"`
	Configs map[string]hostBlock `yaml:"configs"`
}

type mirror struct {
	Endpoint []string          `yaml:"endpoint"`
	Rewrite  map[string]string `yaml:"rewrite"`
}

type hostBlock struct {
	Auth *Auth      `yaml:"auth"`
	TLS  *tlsConfig `yaml:"tls"`
}

type tlsConfig struct {
	CAFile             string `yaml:"ca_file"`
	CertFile           string `yaml:"cert_file"`
	KeyFile            string `yaml:"key_file"`
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify"`
}

func (t *tlsConfig) configured() bool {
	return t != nil && (t.CAFile != "" || t.CertFile != "" || t.KeyFile != "" || t.InsecureSkipVerify)
}

type endpoint struct {
	url      string
	override bool
	rewrites map[string]string
	tls      *tlsConfig
}

type hostSpec struct {
	includeServer bool
	serverURL     string
	serverTLS     *tlsConfig
	endpoints     []endpoint
}

// Prepare writes containerd hosts under dir from the registries file.
// A missing or empty file leaves dir empty so containerd uses upstream endpoints.
// disableDefault omits the upstream registry for names listed in the file.
// Registries that are not listed keep their default endpoint.
// The quoted "*" wildcard is written as the containerd default host and is
// used only when that registry has no entry of its own.
// Auth entries are returned for the containerd config. TLS, mirrors, and
// rewrites are written into the host files.
func Prepare(dir, filePath string, disableDefault bool) (map[string]Auth, error) {
	if dir == "" || dir == "/" || dir == "." {
		return nil, errors.New("registry config directory is required")
	}
	if err := resetDir(dir); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, nil
	}
	body, err := os.ReadFile(filePath) // #nosec G304 -- path is the configured registries file
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read registries %s: %w", filePath, err)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}
	var doc file
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("read registries %s: %w", filePath, err)
	}
	hosts, auths, err := build(filePath, doc, disableDefault)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(hosts))
	for name := range hosts {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		spec := hosts[name]
		if !spec.worthWriting() {
			continue
		}
		hostDir := filepath.Join(dir, hostDirectory(name))
		if err := os.MkdirAll(hostDir, 0o755); err != nil { // #nosec G301 -- containerd reads this host directory
			return nil, fmt.Errorf("create registry config %s: %w", hostDir, err)
		}
		if err := os.WriteFile(filepath.Join(hostDir, "hosts.toml"), render(spec), 0o644); err != nil { // #nosec G306 -- hosts.toml holds endpoints, not credentials
			return nil, fmt.Errorf("write registry config %s: %w", hostDir, err)
		}
	}
	if len(auths) == 0 {
		return nil, nil
	}
	return auths, nil
}

func resetDir(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("reset registry config %s: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil { // #nosec G301 -- containerd reads the registry config directory
		return fmt.Errorf("create registry config %s: %w", dir, err)
	}
	return nil
}

func build(path string, doc file, disableDefault bool) (map[string]hostSpec, map[string]Auth, error) {
	hosts := map[string]hostSpec{}
	for name, cfg := range doc.Configs {
		if name == "" {
			continue
		}
		if name == "*" {
			if cfg.TLS.configured() {
				hosts["*"] = hostSpec{includeServer: true, serverTLS: cfg.TLS}
			}
			continue
		}
		server, err := registryURL(name)
		if err != nil {
			return nil, nil, fmt.Errorf("registries %s: %w", path, err)
		}
		hosts[name] = hostSpec{
			includeServer: true,
			serverURL:     server,
			serverTLS:     cfg.TLS,
		}
	}
	for name, mirror := range doc.Mirrors {
		if name == "" {
			return nil, nil, fmt.Errorf("registries %s: mirror name is empty", path)
		}
		spec, ok := hosts[name]
		if !ok {
			spec = hostSpec{}
			if name == "*" {
				if !disableDefault {
					spec.includeServer = true
					spec.serverTLS = tlsFor(doc, "*")
				}
			} else if !disableDefault {
				server, err := registryURL(name)
				if err != nil {
					return nil, nil, fmt.Errorf("registries %s: %w", path, err)
				}
				spec.includeServer = true
				spec.serverURL = server
				spec.serverTLS = tlsFor(doc, name)
			}
		} else if disableDefault {
			spec.includeServer = false
			spec.serverURL = ""
			spec.serverTLS = nil
		}
		seen := map[string]bool{}
		for i, raw := range mirror.Endpoint {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				return nil, nil, fmt.Errorf("registries %s: mirror %s has an empty endpoint", path, name)
			}
			registryName, endpointURL, override, err := normalizeEndpoint(raw)
			if err != nil {
				return nil, nil, fmt.Errorf("registries %s: mirror %s endpoint %d: %w", path, name, i, err)
			}
			if seen[endpointURL] {
				continue
			}
			seen[endpointURL] = true
			spec.endpoints = append(spec.endpoints, endpoint{
				url:      endpointURL,
				override: override,
				rewrites: copyMap(mirror.Rewrite),
				tls:      tlsFor(doc, registryName),
			})
		}
		if spec.includeServer && len(spec.endpoints) > 0 {
			last := spec.endpoints[len(spec.endpoints)-1]
			if last.url == spec.serverURL && !last.override && len(last.rewrites) == 0 && !last.tls.configured() {
				spec.endpoints = spec.endpoints[:len(spec.endpoints)-1]
			}
		}
		hosts[name] = spec
	}
	auths := map[string]Auth{}
	for name, cfg := range doc.Configs {
		if name == "" || cfg.Auth == nil {
			continue
		}
		if cfg.Auth.Username == "" && cfg.Auth.Password == "" && cfg.Auth.Token == "" {
			continue
		}
		auths[name] = *cfg.Auth
	}
	return hosts, auths, nil
}

func (s hostSpec) worthWriting() bool {
	if len(s.endpoints) > 0 {
		return true
	}
	return s.serverTLS.configured()
}

func tlsFor(doc file, host string) *tlsConfig {
	if cfg, ok := doc.Configs[host]; ok && cfg.TLS.configured() {
		return cfg.TLS
	}
	if normalized, err := docker.DefaultHost(host); err == nil && normalized != host {
		if cfg, ok := doc.Configs[normalized]; ok && cfg.TLS.configured() {
			return cfg.TLS
		}
	}
	if host == "registry-1.docker.io" {
		if cfg, ok := doc.Configs["docker.io"]; ok && cfg.TLS.configured() {
			return cfg.TLS
		}
	}
	return nil
}

func registryURL(host string) (string, error) {
	_, endpointURL, _, err := normalizeEndpoint(host)
	return endpointURL, err
}

func normalizeEndpoint(endpoint string) (registryName, endpointURL string, override bool, err error) {
	raw := endpoint
	if !strings.Contains(endpoint, "://") {
		endpoint = "//" + endpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return "", "", false, fmt.Errorf("invalid registry endpoint %q", raw)
	}
	if parsed.Scheme == "" {
		port := parsed.Port()
		if docker.IsLocalhost(parsed.Host) && port != "" && port != "443" {
			parsed.Scheme = "http"
		} else {
			parsed.Scheme = "https"
		}
	}
	registryName = parsed.Host
	host, err := docker.DefaultHost(registryName)
	if err != nil {
		return "", "", false, err
	}
	parsed.Host = host
	if parsed.Host == "registry-1.docker.io" {
		registryName = "docker.io"
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	switch parsed.Path {
	case "", "/", "/v2":
		parsed.Path = "/v2"
		parsed.RawPath = ""
		return registryName, parsed.String(), false, nil
	default:
		return registryName, parsed.String(), true, nil
	}
}

func hostDirectory(host string) string {
	if host == "*" {
		return "_default"
	}
	idx := strings.LastIndex(host, ":")
	if idx > 0 {
		return host[:idx] + "_" + host[idx+1:] + "_"
	}
	return host
}

func copyMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func render(spec hostSpec) []byte {
	var b strings.Builder
	if spec.includeServer {
		b.WriteString("server = ")
		b.WriteString(strconv.Quote(spec.serverURL))
		b.WriteByte('\n')
		b.WriteString("capabilities = [\"pull\", \"resolve\", \"push\"]\n")
		writeTLS(&b, spec.serverTLS, "")
	}
	for _, ep := range spec.endpoints {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("[host.")
		b.WriteString(strconv.Quote(ep.url))
		b.WriteString("]\n")
		b.WriteString("  capabilities = [\"pull\", \"resolve\"]\n")
		if ep.override {
			b.WriteString("  override_path = true\n")
		}
		writeTLS(&b, ep.tls, "  ")
		if len(ep.rewrites) > 0 {
			b.WriteByte('\n')
			b.WriteString("  [host.")
			b.WriteString(strconv.Quote(ep.url))
			b.WriteString(".rewrite]\n")
			keys := make([]string, 0, len(ep.rewrites))
			for key := range ep.rewrites {
				keys = append(keys, key)
			}
			slices.Sort(keys)
			for _, key := range keys {
				b.WriteString("    ")
				b.WriteString(strconv.Quote(key))
				b.WriteString(" = ")
				b.WriteString(strconv.Quote(ep.rewrites[key]))
				b.WriteByte('\n')
			}
		}
	}
	return []byte(b.String())
}

func writeTLS(b *strings.Builder, tls *tlsConfig, indent string) {
	if !tls.configured() {
		return
	}
	if tls.CAFile != "" {
		b.WriteString(indent)
		b.WriteString("ca = ")
		b.WriteString(strconv.Quote(tls.CAFile))
		b.WriteByte('\n')
	}
	if tls.CertFile != "" || tls.KeyFile != "" {
		b.WriteString(indent)
		b.WriteString("client = [[")
		b.WriteString(strconv.Quote(tls.CertFile))
		b.WriteString(", ")
		b.WriteString(strconv.Quote(tls.KeyFile))
		b.WriteString("]]\n")
	}
	if tls.InsecureSkipVerify {
		b.WriteString(indent)
		b.WriteString("skip_verify = true\n")
	}
}
