package datastore

import (
	"fmt"
	"net/url"
	"strings"

	kinetls "github.com/k3s-io/kine/pkg/tls"
)

// prepare returns the DSN and backend TLS handed to kine.
// A non-nats endpoint is unchanged. On nats://, a credentials file is
// injected as credsFile and URL userinfo is dropped. A credentials file or
// CA adds noEmbed and the scheme stays nats://. The CA, client certificate,
// and key stay in the TLS config. bucket, replicas, and revHistory are left
// on the URL. This does not look up a server name.
func prepare(cfg Config) (string, kinetls.Config, error) {
	tlsCfg := kinetls.Config{
		CAFile:   cfg.CAFile,
		CertFile: cfg.CertFile,
		KeyFile:  cfg.KeyFile,
	}
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if !natsEndpoint(endpoint) {
		return endpoint, tlsCfg, nil
	}
	dsn, err := natsDSN(endpoint, cfg.NATSCreds, cfg.CAFile != "")
	if err != nil {
		return "", kinetls.Config{}, err
	}
	return dsn, tlsCfg, nil
}

func natsEndpoint(endpoint string) bool {
	first, _, _ := strings.Cut(endpoint, ",")
	scheme, _, ok := strings.Cut(first, "://")
	return ok && scheme == "nats"
}

// natsDSN applies external NATS rules. With neither credentials nor a CA,
// the endpoint is returned unchanged, including a bare nats://.
func natsDSN(endpoint, creds string, ca bool) (string, error) {
	if creds == "" && !ca {
		return endpoint, nil
	}
	parts := strings.Split(endpoint, ",")
	rewritten := make([]string, len(parts))
	for i, part := range parts {
		u, err := url.Parse(part)
		if err != nil {
			return "", fmt.Errorf("parse datastore endpoint: %w", err)
		}
		if u.Scheme != "nats" {
			return "", fmt.Errorf("datastore endpoint scheme %s", u.Scheme)
		}
		if i == 0 {
			q := u.Query()
			if creds != "" {
				q.Set("credsFile", creds)
				u.User = nil
			}
			if !q.Has("noEmbed") {
				q.Set("noEmbed", "")
			}
			u.RawQuery = q.Encode()
		} else if creds != "" {
			u.User = nil
		}
		rewritten[i] = u.String()
	}
	return strings.Join(rewritten, ","), nil
}

// sqliteFile returns the filesystem path in a sqlite DSN, without query parameters.
func sqliteFile(dsn string) string {
	_, addr, ok := strings.Cut(dsn, "://")
	if !ok {
		return ""
	}
	if i := strings.Index(addr, "?"); i >= 0 {
		addr = addr[:i]
	}
	return addr
}
