package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func artifactName(path string) (osName, arch string, err error) {
	base := filepath.Base(path)
	trimmed := strings.TrimSuffix(base, ".exe")
	parts := strings.Split(trimmed, "-")
	if len(parts) < 3 || parts[0] != "edgelet" {
		return "", "", fmt.Errorf("unrecognized artifact name %s", base)
	}
	return parts[1], strings.Join(parts[2:], "-"), nil
}

func selectArtifact(comps []Component, osName, arch, version, binaryHash string) (Component, []Component, error) {
	want := map[string]bool{}
	switch osName {
	case "linux":
		want["linux-thin"] = true
		want["linux-fat"] = true
		want["linux"] = true
	case "darwin":
		want["darwin"] = true
	case "windows":
		want["windows"] = true
	default:
		return Component{}, nil, fmt.Errorf("unsupported OS %s", osName)
	}
	var kept []Component
	keepRef := map[string]bool{}
	for _, c := range comps {
		if c.Scope != scopeRequired || !closureMatch(c, want) || !archOK(c, osName, arch) {
			continue
		}
		kept = append(kept, c)
		keepRef[c.Ref] = true
	}
	for i := range kept {
		kept[i].Props = filterContainedBy(kept[i].Props, keepRef)
		kept[i].Scope = scopeRequired
		if kept[i].Relation == relExcluded || kept[i].Relation == relTest {
			kept[i].Relation = relDepends
		}
	}
	subject := Component{
		Ref:      fmt.Sprintf("pkg:generic/edgelet@%s?arch=%s&os=%s", version, arch, osName),
		Type:     "application",
		Name:     "edgelet",
		Version:  version,
		PURL:     fmt.Sprintf("pkg:generic/edgelet@%s?arch=%s&os=%s", version, arch, osName),
		Scope:    scopeRequired,
		License:  "NOASSERTION",
		Hashes:   []Hash{{Alg: "SHA-256", Content: binaryHash}},
		Props:    []Prop{prop("edgelet:os", osName), prop("edgelet:arch", arch)},
		Relation: relDepends,
		Note:     "This document describes one published binary.",
	}
	sortComponents(kept)
	return subject, kept, nil
}

func closureMatch(c Component, want map[string]bool) bool {
	if strings.HasPrefix(c.Ref, "pkg:generic/go@") {
		return true
	}
	for _, cl := range c.Closures {
		if want[cl] {
			return true
		}
	}
	return false
}

func archOK(c Component, osName, arch string) bool {
	got := propValue(c, "edgelet:arch")
	if got == "" {
		return true
	}
	return osName == "linux" && got == arch
}

func propValue(c Component, name string) string {
	for _, p := range c.Props {
		if p.Name == name {
			return p.Value
		}
	}
	return ""
}

func filterContainedBy(props []Prop, keep map[string]bool) []Prop {
	var out []Prop
	for _, p := range props {
		if p.Name == "edgelet:contained-by" && !keep[p.Value] {
			continue
		}
		out = append(out, p)
	}
	return out
}

func applyStage(comps []Component, stage, version, arch string) ([]Component, error) {
	sums, err := readSums(filepath.Join(stage, "bin", ".sha256sums"))
	if err != nil {
		return nil, err
	}
	links, err := readLinks(filepath.Join(stage, "bin", ".links"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	pausePath := filepath.Join(stage, "images", "pause.tar.gz")
	pauseHash, err := hashFile(pausePath)
	if err != nil {
		return nil, fmt.Errorf("pause archive: %w", err)
	}
	setHash := func(name, sum string) error {
		for i := range comps {
			if comps[i].Name == name {
				comps[i].Props = append(comps[i].Props, prop("edgelet:staged-sha256", sum))
				return nil
			}
		}
		return fmt.Errorf("no component named %s for staged hash", name)
	}
	required := map[string]string{
		"crun":                    sums["crun"],
		"cni-plugins":             sums["cni"],
		"containerd-shim-runc-v2": sums["containerd-shim-runc-v2"],
		"busybox":                 sums["busybox"],
		"iproute2":                sums["ip"],
	}
	for name, sum := range required {
		if sum == "" {
			return nil, fmt.Errorf("stage is missing %s", name)
		}
		if err := setHash(name, sum); err != nil {
			return nil, err
		}
	}
	if err := setHash("portainer/pause", pauseHash); err != nil {
		// The pause component name is the repository, not this string.
		if err := setHashByPrefix(comps, "pkg:oci/pause@", pauseHash); err != nil {
			return nil, err
		}
	}
	if sums["edgelet"] == "" {
		return nil, fmt.Errorf("stage is missing the fat runtime bin/edgelet")
	}
	fat := Component{
		Ref:      fmt.Sprintf("pkg:generic/edgelet-fat@%s?arch=%s", version, arch),
		Type:     "application",
		Name:     "edgelet-fat",
		Version:  version,
		PURL:     fmt.Sprintf("pkg:generic/edgelet-fat@%s?arch=%s", version, arch),
		Scope:    scopeRequired,
		License:  "NOASSERTION",
		Relation: relContains,
		Hashes:   []Hash{{Alg: "SHA-256", Content: sums["edgelet"]}},
		Note:     "Fat runtime ELF inside the embed bundle, built from cmd/edgelet-server.",
		Props:    []Prop{prop("edgelet:staged-path", "bin/edgelet")},
	}
	comps = append(comps, fat)
	claimed := map[string]bool{"crun": true, "cni": true, "containerd-shim-runc-v2": true, "busybox": true, "ip": true, "edgelet": true}
	var paths []string
	for path := range sums {
		if !claimed[path] {
			paths = append(paths, path)
		}
	}
	sortStrings(paths)
	for _, path := range paths {
		comps = append(comps, stagedFile(path, sums[path], version, arch))
	}
	var linkPaths []string
	for path := range links {
		linkPaths = append(linkPaths, path)
	}
	sortStrings(linkPaths)
	for _, path := range linkPaths {
		comps = append(comps, stagedLink(path, links[path], version, arch))
	}
	sortComponents(comps)
	return comps, nil
}

func setHashByPrefix(comps []Component, prefix, sum string) error {
	for i := range comps {
		if strings.HasPrefix(comps[i].Ref, prefix) {
			comps[i].Props = append(comps[i].Props, prop("edgelet:staged-sha256", sum))
			return nil
		}
	}
	return fmt.Errorf("no component with ref prefix %s", prefix)
}

func stagedFile(path, sum, version, arch string) Component {
	ref := fmt.Sprintf("pkg:generic/edgelet-staged@%s?arch=%s&path=%s", version, arch, path)
	return Component{
		Ref:      ref,
		Type:     "file",
		Name:     path,
		Version:  version,
		PURL:     ref,
		Scope:    scopeRequired,
		License:  licenseForStaged(path),
		Relation: relContains,
		Hashes:   []Hash{{Alg: "SHA-256", Content: sum}},
		Props:    []Prop{prop("edgelet:staged-path", "bin/"+path)},
		Note:     "File staged into the Linux embed bundle.",
	}
}

func stagedLink(path, target, version, arch string) Component {
	ref := fmt.Sprintf("pkg:generic/edgelet-staged-link@%s?arch=%s&path=%s", version, arch, path)
	return Component{
		Ref:      ref,
		Type:     "file",
		Name:     path,
		Version:  version,
		PURL:     ref,
		Scope:    scopeRequired,
		License:  licenseForStaged(path),
		Relation: relContains,
		Props: []Prop{
			prop("edgelet:staged-path", "bin/"+path),
			prop("edgelet:symlink-target", target),
		},
		Note: "Symlink staged into the Linux embed bundle.",
	}
}

func licenseForStaged(path string) string {
	base := filepath.Base(path)
	switch {
	case path == "busybox" || path == "modprobe" || base == "modprobe":
		return "GPL-2.0-only"
	case path == "ip":
		return "GPL-2.0-only"
	case strings.HasPrefix(base, "iptables"), strings.HasPrefix(base, "ip6tables"), strings.HasPrefix(base, "xtables"), strings.HasPrefix(base, "arptables"):
		return "GPL-2.0-or-later"
	case base == "nft" || strings.HasPrefix(base, "ebtables"):
		return "GPL-2.0-only"
	case base == "mount":
		return "GPL-2.0-or-later"
	default:
		return "NOASSERTION"
	}
}

func readSums(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		sum, name, ok := strings.Cut(line, "  ")
		if !ok || len(sum) != 64 {
			return nil, fmt.Errorf("bad sha256sums line %q", line)
		}
		out[name] = sum
	}
	return out, nil
}

func readLinks(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, target, ok := strings.Cut(line, " ")
		if !ok {
			return nil, fmt.Errorf("bad links line %q", line)
		}
		out[name] = target
	}
	return out, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		j := i
		for j > 0 && s[j] < s[j-1] {
			s[j], s[j-1] = s[j-1], s[j]
			j--
		}
	}
}
