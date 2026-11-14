package charts

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet-kube/scripts/airgap"
	"go.yaml.in/yaml/v3"
)

func TestChartsMatchPins(t *testing.T) {
	names, err := Names()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 4 {
		t.Fatalf("charts %v", names)
	}
	entries, err := files.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(names) {
		t.Fatalf("embedded files %d", len(entries))
	}
	seen := map[string]bool{}
	for _, name := range names {
		for _, bad := range []string{"datasance", "iofog", "potctl"} {
			if strings.Contains(strings.ToLower(name), bad) {
				t.Fatalf("chart %s", name)
			}
		}
		body, err := Read(name)
		if err != nil {
			t.Fatal(err)
		}
		if len(body) < 2 || body[0] != 0x1f || body[1] != 0x8b {
			t.Fatalf("%s is not a gzip tarball", name)
		}
		version := chartVersion(t, body)
		chart := strings.TrimSuffix(name, ".tgz")
		pinName := chart
		for _, candidate := range []string{"traefik-crd", "traefik", "gateway-api-crd", "cert-manager"} {
			if strings.HasPrefix(chart, candidate+"-") {
				pinName = candidate
				break
			}
		}
		if version != airgap.ChartVersion(pinName) {
			t.Fatalf("%s version %s", name, version)
		}
		seen[name] = true
	}
	for _, entry := range entries {
		if !seen[entry.Name()] {
			t.Fatalf("unexpected chart %s", entry.Name())
		}
	}
	for _, image := range airgap.Images() {
		if strings.Contains(image, ".tgz") {
			t.Fatalf("chart tarball is on the image list: %s", image)
		}
	}
}

func TestHandlerServesChartsAndDelegates(t *testing.T) {
	names, err := Names()
	if err != nil {
		t.Fatal(err)
	}
	var delegated []string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delegated = append(delegated, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	})
	handler := Handler(next)
	for _, name := range names {
		body, err := Read(name)
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, Prefix+name, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status %d", name, rec.Code)
		}
		if rec.Header().Get("Content-Type") != "application/gzip" {
			t.Fatalf("%s type %s", name, rec.Header().Get("Content-Type"))
		}
		if !bytes.Equal(rec.Body.Bytes(), body) {
			t.Fatalf("%s body mismatch", name)
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusNoContent || len(delegated) != 1 || delegated[0] != "/readyz" {
		t.Fatalf("delegate status %d paths %v", rec.Code, delegated)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, Prefix+"other.tgz", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown chart status %d", rec.Code)
	}
}

func chartVersion(t *testing.T, body []byte) string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(hdr.Name, "/Chart.yaml") && hdr.Name != "Chart.yaml" {
			continue
		}
		raw, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Version string `yaml:"version"`
		}
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		return doc.Version
	}
	t.Fatal("Chart.yaml was not in the tarball")
	return ""
}
