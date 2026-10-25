package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type resolvedMod struct {
	ImportPath string
	Path       string
	Version    string
	Dir        string
}

type closureSpec struct {
	Name  string
	Env   []string
	Pkgs  []string
	Test  bool
	Allow bool // -e: package errors do not fail the listing
}

func buildInventory(repo string) ([]Component, Component, error) {
	pins, err := parsePins(repo + "/scripts/version.sh")
	if err != nil {
		return nil, Component{}, err
	}
	if err := ensureModules(repo); err != nil {
		return nil, Component{}, err
	}
	mods, err := listModules(repo)
	if err != nil {
		return nil, Component{}, err
	}
	required := map[string][]string{}
	for _, spec := range runtimeClosures() {
		paths, err := modulePaths(repo, spec)
		if err != nil {
			return nil, Component{}, err
		}
		for path := range paths {
			required[path] = append(required[path], spec.Name)
		}
	}
	tested := map[string]struct{}{}
	for _, spec := range testClosures() {
		paths, err := modulePaths(repo, spec)
		if err != nil {
			return nil, Component{}, err
		}
		for path := range paths {
			if _, ok := required[path]; ok {
				continue
			}
			tested[path] = struct{}{}
		}
	}

	gpl, err := readNotice(repo, "gpl-2.0.txt")
	if err != nil {
		return nil, Component{}, err
	}
	goLicense, err := readNotice(repo, "go-bsd.txt")
	if err != nil {
		return nil, Component{}, err
	}

	byRef := map[string]*Component{}
	var order []string
	add := func(c Component) {
		if prev, ok := byRef[c.Ref]; ok {
			prev.Closures = uniqueStrings(append(prev.Closures, c.Closures...))
			return
		}
		cp := c
		byRef[c.Ref] = &cp
		order = append(order, c.Ref)
	}

	var containerdPath, containerdVersion string
	for importPath, mod := range mods {
		if importPath == "github.com/eclipse-iofog/edgelet" {
			continue
		}
		comp, err := moduleComponent(mod)
		if err != nil {
			return nil, Component{}, err
		}
		switch {
		case len(required[importPath]) > 0:
			comp.Scope = scopeRequired
			comp.Relation = relDepends
			comp.Closures = uniqueStrings(required[importPath])
		case tested[importPath] != struct{}{}:
			comp.Scope = scopeOptional
			comp.Relation = relTest
		default:
			comp.Scope = scopeExcluded
			comp.Relation = relExcluded
		}
		if strings.HasPrefix(comp.Ref, "pkg:golang/github.com/k3s-io/containerd/v2@") {
			containerdPath = mod.Path
			containerdVersion = mod.Version
		}
		add(comp)
		if mod.Path == "modernc.org/sqlite" {
			sqlite, err := sqliteComponent(mod, comp.Ref, comp.Scope, comp.Closures)
			if err != nil {
				return nil, Component{}, err
			}
			add(sqlite)
		}
	}
	if containerdVersion == "" {
		return nil, Component{}, fmt.Errorf("containerd module is not in the build list")
	}
	for _, c := range embedComponents(pins, containerdPath, containerdVersion, gpl) {
		add(c)
	}
	add(Component{
		Ref:         "pkg:generic/go@" + pins.Go,
		Type:        "library",
		Name:        "Go",
		Version:     pins.Go,
		PURL:        "pkg:generic/go@" + pins.Go,
		Scope:       scopeRequired,
		License:     "BSD-3-Clause",
		LicenseText: goLicense,
		Relation:    relDepends,
		Note:        "Toolchain pin from scripts/version.sh. The SBOM does not record the patch level of the machine that generated it.",
	})

	comps := make([]Component, 0, len(order))
	for _, ref := range order {
		comps = append(comps, *byRef[ref])
	}
	sortComponents(comps)
	subject := Component{
		Ref:     "pkg:golang/github.com/eclipse-iofog/edgelet",
		Type:    "application",
		Name:    "edgelet",
		Version: "NOASSERTION",
		PURL:    "pkg:golang/github.com/eclipse-iofog/edgelet",
		Scope:   scopeRequired,
		License: "NOASSERTION",
		Note:    "Source tree. This document does not describe one built binary.",
	}
	return comps, subject, nil
}

func moduleComponent(mod resolvedMod) (Component, error) {
	text, err := readLicenseText(mod.Dir)
	if err != nil {
		return Component{}, fmt.Errorf("license files in %s: %w", mod.Path, err)
	}
	license := "NOASSERTION"
	if strings.TrimSpace(text) != "" {
		license = detectLicense(text)
	}
	ref := golangPURL(mod.Path, mod.Version)
	c := Component{
		Ref:         ref,
		Type:        "library",
		Name:        mod.Path,
		Version:     mod.Version,
		PURL:        ref,
		License:     license,
		LicenseText: text,
	}
	if mod.ImportPath != mod.Path {
		c.Props = append(c.Props, prop("edgelet:import-path", mod.ImportPath))
	}
	return c, nil
}

func sqliteComponent(mod resolvedMod, parent, parentScope string, closures []string) (Component, error) {
	ver, err := readSQLiteVersion(mod.Dir)
	if err != nil {
		return Component{}, fmt.Errorf("SQLITE_VERSION in %s: %w", mod.Path, err)
	}
	raw, err := os.ReadFile(filepath.Join(mod.Dir, "LICENSE-SQLITE"))
	if err != nil {
		return Component{}, fmt.Errorf("LICENSE-SQLITE: %w", err)
	}
	text := normalizeNewlines(string(raw))
	return Component{
		Ref:         "pkg:generic/sqlite@" + ver,
		Type:        "library",
		Name:        "SQLite",
		Version:     ver,
		PURL:        "pkg:generic/sqlite@" + ver,
		Scope:       parentScope,
		License:     "LicenseRef-SQLite-PublicDomain",
		LicenseText: text,
		Closures:    append([]string(nil), closures...),
		Relation:    relContains,
		ContainedBy: parent,
		Props:       []Prop{prop("edgelet:contained-by", parent)},
		Note:        "Linked inside modernc.org/sqlite. The version is the SQLITE_VERSION constant in that module, which go list does not report.",
	}, nil
}

func golangPURL(path, version string) string {
	return "pkg:golang/" + path + "@" + strings.ReplaceAll(version, "+", "%2B")
}

func ensureModules(repo string) error {
	cmd := exec.Command("go", "mod", "download", "all")
	cmd.Dir = repo
	cmd.Env = filteredEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go mod download: %w\n%s", err, stderr.String())
	}
	return nil
}

func listModules(repo string) (map[string]resolvedMod, error) {
	raw, err := runGo(repo, nil, "list", "-m", "-json", "all")
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	out := map[string]resolvedMod{}
	for dec.More() {
		var row struct {
			Path    string
			Version string
			Dir     string
			Main    bool
			Replace *struct {
				Path    string
				Version string
				Dir     string
			}
		}
		if err := dec.Decode(&row); err != nil {
			return nil, err
		}
		if row.Main {
			continue
		}
		mod := resolvedMod{ImportPath: row.Path, Path: row.Path, Version: row.Version, Dir: row.Dir}
		if row.Replace != nil && row.Replace.Path != "" {
			mod.Path = row.Replace.Path
			if row.Replace.Version != "" {
				mod.Version = row.Replace.Version
			}
			if row.Replace.Dir != "" {
				mod.Dir = row.Replace.Dir
			}
		}
		if mod.Dir == "" {
			return nil, fmt.Errorf("module %s has no downloaded dir", row.Path)
		}
		out[row.Path] = mod
	}
	return out, nil
}

func modulePaths(repo string, spec closureSpec) (map[string]struct{}, error) {
	args := []string{"list", "-deps", "-f", "{{if .Module}}{{.Module.Path}}{{end}}"}
	if spec.Allow {
		args = []string{"list", "-e", "-deps", "-f", "{{if .Module}}{{.Module.Path}}{{end}}"}
	}
	if spec.Test {
		args = append(args, "-test")
	}
	args = append(args, spec.Pkgs...)
	raw, err := runGo(repo, spec.Env, args...)
	if err != nil {
		return nil, fmt.Errorf("closure %s: %w", spec.Name, err)
	}
	out := map[string]struct{}{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out[line] = struct{}{}
		}
	}
	return out, nil
}

func runtimeClosures() []closureSpec {
	return []closureSpec{
		{Name: "linux-thin", Env: []string{"GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0"}, Pkgs: []string{"./cmd/edgelet"}},
		{Name: "linux-fat", Env: []string{"GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=1"}, Pkgs: []string{"./cmd/edgelet-server"}},
		{Name: "darwin", Env: []string{"GOOS=darwin", "GOARCH=amd64", "CGO_ENABLED=0"}, Pkgs: []string{"./cmd/edgelet"}},
		{Name: "windows", Env: []string{"GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0"}, Pkgs: []string{"./cmd/edgelet"}},
	}
}

func testClosures() []closureSpec {
	return []closureSpec{
		{Name: "test-linux", Env: []string{"GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0"}, Pkgs: []string{"./..."}, Test: true, Allow: true},
		{Name: "test-linux-cgo", Env: []string{"GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=1"}, Pkgs: []string{"./cmd/edgelet-server", "./pkg/containerd/..."}, Test: true, Allow: true},
		{Name: "test-darwin", Env: []string{"GOOS=darwin", "GOARCH=amd64", "CGO_ENABLED=0"}, Pkgs: []string{"./..."}, Test: true, Allow: true},
		{Name: "test-windows", Env: []string{"GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0"}, Pkgs: []string{"./..."}, Test: true, Allow: true},
	}
}

func runGo(repo string, extra []string, args ...string) ([]byte, error) {
	cmd := exec.Command("go", args...)
	cmd.Dir = repo
	cmd.Env = filteredEnv()
	cmd.Env = append(cmd.Env, "GOFLAGS=-mod=readonly")
	cmd.Env = append(cmd.Env, extra...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go %s: %w\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.Bytes(), nil
}

func filteredEnv() []string {
	var out []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "GOFLAGS=") || strings.HasPrefix(e, "GOOS=") || strings.HasPrefix(e, "GOARCH=") || strings.HasPrefix(e, "CGO_ENABLED=") {
			continue
		}
		out = append(out, e)
	}
	return out
}

func readNotice(repo, name string) (string, error) {
	raw, err := os.ReadFile(repo + "/licensegen/notice/" + name)
	if err != nil {
		return "", err
	}
	return normalizeNewlines(string(raw)), nil
}
