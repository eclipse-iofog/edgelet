package charts

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"github.com/eclipse-iofog/edgelet-kube/scripts/airgap"
)

//go:embed *.tgz
var files embed.FS

// Prefix is the API server path the chart tarballs are served on.
const Prefix = "/static/charts/"

// Names returns the embedded chart file names. The versions come from the pin file.
func Names() ([]string, error) {
	var names []string
	for _, chart := range []string{"traefik", "traefik-crd", "gateway-api-crd", "cert-manager"} {
		version := airgap.ChartVersion(chart)
		if version == "" {
			return nil, fmt.Errorf("chart %s has no pinned version", chart)
		}
		names = append(names, chart+"-"+version+".tgz")
	}
	return names, nil
}

// Read returns one embedded chart tarball.
func Read(name string) ([]byte, error) {
	names, err := Names()
	if err != nil {
		return nil, err
	}
	if !listed(names, name) {
		return nil, fs.ErrNotExist
	}
	body, err := files.ReadFile(name)
	if err != nil {
		return nil, err
	}
	return body, nil
}

// Handler serves Prefix and delegates every other request to next.
func Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := strings.CutPrefix(r.URL.Path, Prefix)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := Read(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(body) // #nosec G705 -- response is an embedded chart archive, not HTML
	})
}

func listed(names []string, name string) bool {
	if name == "" || strings.Contains(name, "/") {
		return false
	}
	for _, item := range names {
		if item == name {
			return true
		}
	}
	return false
}
