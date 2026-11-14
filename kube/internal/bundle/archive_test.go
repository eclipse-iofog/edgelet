package bundle

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet-kube/scripts/airgap"
	"github.com/eclipse-iofog/edgelet/pkg/untar"
)

func TestPackListsMembers(t *testing.T) {
	packed := packStage(t, writeStage(t))
	got := listMembers(t, packed)
	const want = `bin/
bin/aux/
bin/aux/ip6tables -> xtables-nft-multi
bin/aux/ip6tables-restore -> xtables-nft-multi
bin/aux/ip6tables-save -> xtables-nft-multi
bin/aux/iptables -> xtables-nft-multi
bin/aux/iptables-restore -> xtables-nft-multi
bin/aux/iptables-save -> xtables-nft-multi
bin/aux/xtables-nft-multi
bin/bridge -> cni
bin/busybox
bin/cni
bin/containerd-shim-runc-v2
bin/crun
bin/edgelet-kube
bin/host-local -> cni
bin/ip
bin/loopback -> cni
bin/modprobe -> busybox
bin/portmap -> cni
`
	if got != want {
		t.Fatalf("bundle members:\n%s", got)
	}
	for _, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		name := line
		if i := strings.Index(name, " -> "); i >= 0 {
			name = name[:i]
		}
		name = strings.TrimSuffix(name, "/")
		if ociName(name) {
			t.Fatalf("OCI image layer %s", line)
		}
	}

	dir := t.TempDir()
	if err := untar.Untar(bytes.NewReader(packed), dir); err != nil {
		t.Fatal(err)
	}
	assertLink(t, filepath.Join(dir, "bin", "aux", "iptables"), "xtables-nft-multi")
	assertLink(t, filepath.Join(dir, "bin", "aux", "iptables-save"), "xtables-nft-multi")
	assertLink(t, filepath.Join(dir, "bin", "aux", "iptables-restore"), "xtables-nft-multi")
	assertLink(t, filepath.Join(dir, "bin", "modprobe"), "busybox")
	for _, name := range []string{"bridge", "host-local", "loopback", "portmap"} {
		assertLink(t, filepath.Join(dir, "bin", name), "cni")
	}
	body, err := os.ReadFile(filepath.Join(dir, "bin", "crun"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "crun\n" {
		t.Fatalf("crun body %q", body)
	}
}

func TestPackKeepsSelectiveAux(t *testing.T) {
	dir := writeStage(t)
	extra := filepath.Join(dir, "bin", "aux", "iptables-apply")
	if err := os.WriteFile(extra, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := listMembers(t, packStage(t, dir))
	if !strings.Contains(got, "bin/aux/iptables-apply\n") {
		t.Fatalf("bundle members:\n%s", got)
	}
	if !strings.Contains(got, "bin/aux/iptables -> xtables-nft-multi\n") {
		t.Fatalf("bundle members:\n%s", got)
	}
}

// func TestEmbeddedRuntimeHasNoImage(t *testing.T) {
// 	matches, err := filepath.Glob(filepath.Join("..", "..", "cmd", "edgelet-kube", "embed", "*.tar.zst"))
// 	if err != nil {
// 		t.Fatal(err)
// 	}
// 	if len(matches) != 1 {
// 		t.Fatalf("embed archives %v", matches)
// 	}
// 	if strings.Contains(filepath.Base(matches[0]), "airgap") {
// 		t.Fatalf("embed name %s", matches[0])
// 	}
// 	body, err := os.ReadFile(matches[0])
// 	if err != nil {
// 		t.Fatal(err)
// 	}
// 	members, err := Contents(body)
// 	if err != nil {
// 		t.Fatal(err)
// 	}
// 	if len(members) == 0 {
// 		t.Fatal("embed archive is empty")
// 	}
// 	for _, member := range members {
// 		if imageMember(member.Name) || strings.Contains(member.Name, "airgap") || strings.Contains(member.Name, "pause") {
// 			t.Fatalf("image in bundle: %s", member.Name)
// 		}
// 	}
// }

func TestPackRejectsAirgapArchive(t *testing.T) {
	dir := writeStage(t)
	if err := os.WriteFile(filepath.Join(dir, "bin", airgap.AssetAMD64), []byte("images"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Pack(dir)
	if err == nil || !strings.Contains(err.Error(), "OCI image") {
		t.Fatalf("got %v", err)
	}
}

func TestPackRejectsOCIImage(t *testing.T) {
	dir := writeStage(t)
	image := filepath.Join(dir, "images")
	if err := os.MkdirAll(image, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(image, "pause.tar.gz"), []byte("not-an-image"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Pack(dir)
	if err == nil || !strings.Contains(err.Error(), "OCI image") {
		t.Fatalf("got %v", err)
	}
}

func TestPackRejectsImageInsideAux(t *testing.T) {
	dir := writeStage(t)
	if err := os.WriteFile(filepath.Join(dir, "bin", "aux", "pause.tar"), []byte("layer"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Pack(dir)
	if err == nil || !strings.Contains(err.Error(), "OCI image") {
		t.Fatalf("got %v", err)
	}
}

func TestPackRejectsFlannel(t *testing.T) {
	dir := writeStage(t)
	if err := os.Symlink("cni", filepath.Join(dir, "bin", "flannel")); err != nil {
		t.Fatal(err)
	}
	_, err := Pack(dir)
	if err == nil || !strings.Contains(err.Error(), "flannel") {
		t.Fatalf("got %v", err)
	}
}

func TestPackRejectsExtraCNIPlugin(t *testing.T) {
	dir := writeStage(t)
	if err := os.Symlink("cni", filepath.Join(dir, "bin", "tuning")); err != nil {
		t.Fatal(err)
	}
	_, err := Pack(dir)
	if err == nil || !strings.Contains(err.Error(), "not a runtime binary") {
		t.Fatalf("got %v", err)
	}
}

func packStage(t *testing.T, dir string) []byte {
	t.Helper()
	packed, err := Pack(dir)
	if err != nil {
		t.Fatal(err)
	}
	return packed
}

func writeStage(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(filepath.Join(bin, "aux"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := []string{
		"edgelet-kube",
		"containerd-shim-runc-v2",
		"crun",
		"cni",
		"ip",
		"busybox",
		filepath.Join("aux", "xtables-nft-multi"),
	}
	for _, name := range files {
		path := filepath.Join(bin, name)
		if err := os.WriteFile(path, []byte(filepath.Base(name)+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	links := []struct{ name, target string }{
		{"bridge", "cni"},
		{"host-local", "cni"},
		{"loopback", "cni"},
		{"portmap", "cni"},
		{"modprobe", "busybox"},
		{filepath.Join("aux", "iptables"), "xtables-nft-multi"},
		{filepath.Join("aux", "iptables-save"), "xtables-nft-multi"},
		{filepath.Join("aux", "iptables-restore"), "xtables-nft-multi"},
		{filepath.Join("aux", "ip6tables"), "xtables-nft-multi"},
		{filepath.Join("aux", "ip6tables-save"), "xtables-nft-multi"},
		{filepath.Join("aux", "ip6tables-restore"), "xtables-nft-multi"},
	}
	for _, link := range links {
		if err := os.Symlink(link.target, filepath.Join(bin, link.name)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func listMembers(t *testing.T, packed []byte) string {
	t.Helper()
	members, err := Contents(packed)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, m := range members {
		switch {
		case m.Dir:
			fmt.Fprintf(&b, "%s/\n", m.Name)
		case m.Link != "":
			fmt.Fprintf(&b, "%s -> %s\n", m.Name, m.Link)
		default:
			fmt.Fprintf(&b, "%s\n", m.Name)
		}
	}
	return b.String()
}

func assertLink(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.Readlink(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s -> %s, want %s", path, got, want)
	}
}

func ociName(name string) bool {
	base := filepath.Base(name)
	switch base {
	case "oci-layout", "index.json", "manifest.json", "pause.tar", "pause.tar.gz":
		return true
	}
	if strings.HasSuffix(base, ".tar") || strings.HasSuffix(base, ".tar.gz") || strings.HasSuffix(base, ".tar.zst") {
		return true
	}
	if strings.HasPrefix(name, "images/") || strings.Contains(name, "/images/") {
		return true
	}
	if strings.HasPrefix(name, "blobs/") || strings.Contains(name, "/blobs/") {
		return true
	}
	return false
}
