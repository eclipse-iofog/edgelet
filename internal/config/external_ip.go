package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
)

const (
	externalIPModuleName = "Config External IP"
	defaultExternalIPURL = "https://ipv4-check-perf.radar.cloudflare.com/api/info"
	externalIPTimeout    = 10 * time.Second
	maxExternalIPBody    = 64 * 1024
)

var (
	externalIPLookupURL = defaultExternalIPURL
	externalIPClient    = &http.Client{Timeout: externalIPTimeout}
	// runExternalIPRefresh performs one lookup. Tests replace it to avoid network calls.
	runExternalIPRefresh = func() {
		if err := RefreshIPAddressExternal(context.Background()); err != nil {
			logging.LogWarn(externalIPModuleName, fmt.Sprintf("external address lookup failed: %v", err))
		}
	}
)

// ScheduleIPAddressExternalRefresh looks up the public address in the background.
// A failed lookup leaves the previous address in place and does not block the caller.
func ScheduleIPAddressExternalRefresh() {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logging.LogError(externalIPModuleName, "Panic while refreshing external address", fmt.Errorf("%v", r))
			}
		}()
		runExternalIPRefresh()
	}()
}

// RefreshIPAddressExternal sets IPAddressExternal from the public-address lookup.
// It does not change GPS coordinates and does not write the config file.
func RefreshIPAddressExternal(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, externalIPTimeout)
	defer cancel()

	ip, err := fetchExternalIP(ctx)
	if err != nil {
		return err
	}

	GetInstance().setIPAddressExternal(ip)
	logging.LogDebug(externalIPModuleName, fmt.Sprintf("Updated external IP address: %s", ip))
	return nil
}

func (c *Config) setIPAddressExternal(ip string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.IPAddressExternal = ip
}

// GetIPAddressExternal returns the in-memory public address.
func (c *Config) GetIPAddressExternal() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.IPAddressExternal
}

func fetchExternalIP(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, externalIPLookupURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create external address request: %w", err)
	}
	resp, err := externalIPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to get external address: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected external address status code: %d", resp.StatusCode)
	}
	if ct := strings.ToLower(resp.Header.Get("Content-Type")); ct != "" && !strings.Contains(ct, "json") {
		return "", fmt.Errorf("external address provider returned non-JSON content type %q", resp.Header.Get("Content-Type"))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxExternalIPBody))
	if err != nil {
		return "", fmt.Errorf("failed to read external address response: %w", err)
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] == '<' {
		return "", errors.New("external address provider returned an unexpected response")
	}

	var payload struct {
		IPAddress string `json:"ip_address"`
	}
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return "", fmt.Errorf("failed to parse external address response: %w", err)
	}
	ip := strings.TrimSpace(payload.IPAddress)
	if net.ParseIP(ip) == nil {
		return "", errors.New("external address provider returned an invalid ip_address")
	}
	return ip, nil
}
