// Package airgap is the pinned image list shared by online pull and the airgap archive.
package airgap

import (
	"bufio"
	_ "embed"
	"strings"
)

//go:embed image-list.txt
var imageList string

// Images returns the pinned image references, one per line.
// amd64, arm64, and arm package and pull this list.
func Images() []string {
	return parseList(imageList)
}

// ImagesFor returns the images packaged and pulled for arch.
// riscv64 omits metrics-server and the cert-manager images.
func ImagesFor(arch string) []string {
	images := Images()
	if arch != "riscv64" {
		return images
	}
	out := make([]string, 0, len(images))
	for _, ref := range images {
		if omittedOnRISCV(ref) {
			continue
		}
		out = append(out, ref)
	}
	return out
}

func parseList(body string) []string {
	var out []string
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

func omittedOnRISCV(ref string) bool {
	name, _, _ := strings.Cut(ref, "@")
	repo, _, _ := strings.Cut(name, ":")
	switch {
	case repo == "metrics-server/metrics-server", strings.HasSuffix(repo, "/metrics-server/metrics-server"):
		return true
	case strings.HasPrefix(repo, "cert-manager-"), strings.Contains(repo, "/cert-manager-"):
		return true
	default:
		return false
	}
}
