// Package registrytls builds TLS and HTTP clients for container registries.
package registrytls

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

// ErrHTTPRequiresInsecure is returned when a registry URL uses http:// without insecure=true.
var ErrHTTPRequiresInsecure = errors.New("http registry urls require insecure=true")

// Endpoint is the HTTP identity of a registry URL.
type Endpoint struct {
	// Host is host[:port] with no scheme.
	Host string
	// Scheme is http or https.
	Scheme string
	// PlainHTTP is true when the registry must be contacted over HTTP.
	PlainHTTP bool
}

func decodeCAB64(caB64 string) ([]byte, error) {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, caB64)
	if cleaned == "" {
		return nil, nil
	}
	raw, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(cleaned)
		if err != nil {
			return nil, fmt.Errorf("decode registry ca: %w", err)
		}
	}
	return raw, nil
}

// Parse returns the scheme, host, and plain-HTTP flag for a registry URL.
// http:// requires insecure=true. An empty URL is treated as https with no host.
func Parse(reg *models.Registry) (Endpoint, error) {
	if reg == nil {
		return Endpoint{}, errors.New("registry is required")
	}
	raw := strings.TrimSpace(reg.URL)
	if raw == "" {
		return Endpoint{Scheme: "https"}, nil
	}

	scheme := "https"
	host := raw
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return Endpoint{}, fmt.Errorf("parse registry url: %w", err)
		}
		if u.Host == "" {
			return Endpoint{}, fmt.Errorf("registry url %q has no host", raw)
		}
		scheme = strings.ToLower(u.Scheme)
		host = u.Host
	}
	host = strings.TrimSuffix(host, "/")

	plainHTTP := false
	switch scheme {
	case "http":
		if !reg.Insecure {
			return Endpoint{}, ErrHTTPRequiresInsecure
		}
		plainHTTP = true
	case "https", "":
		scheme = "https"
	default:
		return Endpoint{}, fmt.Errorf("unsupported registry url scheme %q", scheme)
	}

	return Endpoint{
		Host:      host,
		Scheme:    scheme,
		PlainHTTP: plainHTTP,
	}, nil
}

// TLSConfig returns TLS settings for a registry. Extra ca PEM is appended to
// the system pool (it does not replace system CAs). insecure skips verify.
func TLSConfig(reg *models.Registry) (*tls.Config, error) {
	if reg == nil {
		return nil, errors.New("registry is required")
	}
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}
	if reg.Insecure {
		cfg.InsecureSkipVerify = true // #nosec G402 -- controlled by registry insecure flag
	}
	pem, err := decodeCAB64(reg.CAB64)
	if err != nil {
		return nil, err
	}
	if len(pem) == 0 {
		return cfg, nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("registry ca is not a valid PEM certificate bundle")
	}
	cfg.RootCAs = pool
	return cfg, nil
}

// Transport returns an HTTP transport that applies registry ca and insecure.
func Transport(reg *models.Registry) (*http.Transport, error) {
	tlsCfg, err := TLSConfig(reg)
	if err != nil {
		return nil, err
	}
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		TLSClientConfig:     tlsCfg,
		ForceAttemptHTTP2:   true,
	}, nil
}

// HTTPClient returns an HTTP client that applies registry ca and insecure.
func HTTPClient(reg *models.Registry) (*http.Client, error) {
	transport, err := Transport(reg)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: transport}, nil
}
