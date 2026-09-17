package hf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/registrytls"
)

const (
	defaultHubHost = "huggingface.co"
	userAgent      = "edgelet-model-pull"
)

// File is one repository path at a revision.
type File struct {
	Path string
	Size int64
}

// Info is Hub metadata for a repository revision.
type Info struct {
	SHA   string
	Files []File
}

type hubClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

type siblingJSON struct {
	RFilename string `json:"rfilename"`
	Filename  string `json:"filename"`
	Size      int64  `json:"size"`
}

type modelInfoJSON struct {
	SHA      string        `json:"sha"`
	Siblings []siblingJSON `json:"siblings"`
}

func parseHubBase(reg *models.Registry) (string, error) {
	if reg == nil {
		return "", errors.New("registry is required")
	}
	raw := strings.TrimSpace(reg.URL)
	if raw == "" {
		raw = "https://" + defaultHubHost
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse registry url: %w", err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("registry url %q has no host", raw)
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "http":
		if !reg.Insecure {
			return "", errors.New("http registry urls require insecure=true")
		}
	case "https", "":
		scheme = "https"
	default:
		return "", fmt.Errorf("unsupported registry url scheme %q", scheme)
	}
	return scheme + "://" + strings.TrimSuffix(u.Host, "/"), nil
}

func newHubClient(reg *models.Registry, httpClient *http.Client) (*hubClient, error) {
	base, err := parseHubBase(reg)
	if err != nil {
		return nil, err
	}
	if httpClient == nil {
		httpClient, err = registrytls.HTTPClient(reg)
		if err != nil {
			return nil, err
		}
	}
	return &hubClient{
		baseURL:    base,
		token:      strings.TrimSpace(reg.Password),
		httpClient: httpClient,
	}, nil
}

func (c *hubClient) ModelInfo(ctx context.Context, repo, revision string) (Info, error) {
	primary := c.baseURL + hubAPIPath(repo, revision)
	info, err := c.getModelInfo(ctx, primary)
	if err == nil {
		return info, nil
	}
	fallback := c.baseURL + "/api/models/" + encodePath(repo) + "?revision=" + url.QueryEscape(revision)
	alt, fallbackErr := c.getModelInfo(ctx, fallback)
	if fallbackErr == nil {
		return alt, nil
	}
	return Info{}, err
}

func (c *hubClient) getModelInfo(ctx context.Context, rawURL string) (Info, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Info{}, fmt.Errorf("create hub request: %w", err)
	}
	c.applyHeaders(req)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Info{}, fmt.Errorf("hub request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Info{}, fmt.Errorf("read hub response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Info{}, hubStatusError("list repository files", resp.StatusCode, body)
	}
	var parsed modelInfoJSON
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Info{}, fmt.Errorf("decode hub model info: %w", err)
	}
	info := Info{SHA: strings.TrimSpace(parsed.SHA)}
	for _, sib := range parsed.Siblings {
		path := strings.TrimSpace(sib.RFilename)
		if path == "" {
			path = strings.TrimSpace(sib.Filename)
		}
		path = strings.TrimPrefix(strings.ReplaceAll(path, "\\", "/"), "/")
		if path == "" {
			continue
		}
		info.Files = append(info.Files, File{Path: path, Size: sib.Size})
	}
	return info, nil
}

func (c *hubClient) FetchBytes(ctx context.Context, repo, revision, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.resolveURL(repo, revision, path), nil)
	if err != nil {
		return nil, fmt.Errorf("create hub download request: %w", err)
	}
	c.applyHeaders(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hub download %s: %w", path, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("read hub file %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, hubStatusError("download "+path, resp.StatusCode, body)
	}
	return body, nil
}

func (c *hubClient) resolveURL(repo, revision, path string) string {
	return c.baseURL + "/" + encodePath(repo) + "/resolve/" + encodePath(revision) + "/" + encodePath(path)
}

func (c *hubClient) applyHeaders(req *http.Request) {
	req.Header.Set("User-Agent", userAgent)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

func hubAPIPath(repo, revision string) string {
	return "/api/models/" + encodePath(repo) + "/revision/" + encodePath(revision)
}

func encodePath(p string) string {
	p = strings.Trim(strings.ReplaceAll(p, "\\", "/"), "/")
	if p == "" {
		return ""
	}
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func hubStatusError(op string, status int, body []byte) error {
	msg := strings.TrimSpace(string(body))
	if len(msg) > 240 {
		msg = msg[:240]
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%s: hub authentication failed (HTTP %d)", op, status)
	case http.StatusNotFound:
		return fmt.Errorf("%s: not found on hub (HTTP %d)", op, status)
	default:
		if msg == "" {
			return fmt.Errorf("%s: hub returned HTTP %d", op, status)
		}
		return fmt.Errorf("%s: hub returned HTTP %d: %s", op, status, msg)
	}
}
