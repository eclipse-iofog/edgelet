// Package paths is the on-disk layout of the runtime.
// Kubelet reads CNIConfDir and CNIBinDir as --cni-conf-dir and --cni-bin-dir.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// ContainerdSocket is the containerd gRPC socket.
	ContainerdSocket = "/run/edgelet-kube/containerd.sock"
	// ContainerdRoot is the containerd persistent directory.
	ContainerdRoot = "/var/lib/edgelet-kube/agent/containerd"
	// ContainerdState is the containerd transient directory.
	// It is the directory that holds ContainerdSocket.
	ContainerdState = "/run/edgelet-kube"
	// CNIConfDir is the CNI configuration directory.
	// Kubelet receives it as --cni-conf-dir.
	CNIConfDir = "/var/lib/edgelet-kube/agent/etc/cni/net.d"
	// ImagesDir is the directory of image archives imported into containerd.
	// An empty directory pulls the pinned image list.
	ImagesDir = "/var/lib/edgelet-kube/agent/images"
)

// Layout is the runtime directories for one extracted bundle.
type Layout struct {
	// Socket is the containerd gRPC socket.
	Socket string
	// Root is the containerd persistent directory.
	Root string
	// State is the containerd transient directory.
	State string
	// CNIConfDir is the CNI configuration directory kubelet receives as --cni-conf-dir.
	CNIConfDir string
	// CNIBinDir is the CNI plugin directory in the extracted bundle.
	// Kubelet receives it as --cni-bin-dir.
	CNIBinDir string
	// BundledBin is the aux directory in the extracted bundle.
	// prefer-bundled-bin puts this directory ahead of host binaries.
	BundledBin string
}

// FromBundle returns the runtime layout for an extracted bundle directory.
func FromBundle(bundleDir string) (Layout, error) {
	bin := filepath.Join(bundleDir, "bin")
	layout := Layout{
		Socket:     ContainerdSocket,
		Root:       ContainerdRoot,
		State:      ContainerdState,
		CNIConfDir: CNIConfDir,
		CNIBinDir:  bin,
		BundledBin: filepath.Join(bin, "aux"),
	}
	if err := layout.check(); err != nil {
		return Layout{}, err
	}
	return layout, nil
}

// KubeletCNIArgs are the kubelet flags for this layout's CNI directories.
func (l Layout) KubeletCNIArgs() []string {
	return []string{
		"--cni-conf-dir=" + l.CNIConfDir,
		"--cni-bin-dir=" + l.CNIBinDir,
	}
}

// BinPath is PATH for kubelet and kube-proxy.
// prefer puts BundledBin first. iptables, iptables-save, and iptables-restore
// in that directory name xtables-nft-multi.
// When prefer is false, host directories stay first and BundledBin is appended.
func (l Layout) BinPath(prefer bool, hostPath string) string {
	bundled := filepath.Clean(l.BundledBin)
	if bundled == "" || bundled == "." {
		return hostPath
	}
	sep := string(os.PathListSeparator)
	host := make([]string, 0)
	if hostPath != "" {
		for _, part := range strings.Split(hostPath, sep) {
			if part == "" || filepath.Clean(part) == bundled {
				continue
			}
			host = append(host, part)
		}
	}
	if !prefer {
		return strings.Join(append(host, bundled), sep)
	}
	return strings.Join(append([]string{bundled}, host...), sep)
}

func (l Layout) check() error {
	if filepath.Clean(l.CNIBinDir) == "/opt/cni/bin" {
		return fmt.Errorf("unsupported cni binary directory %s", l.CNIBinDir)
	}
	switch filepath.Clean(l.Socket) {
	case "/run/containerd/containerd.sock", "/var/run/containerd/containerd.sock":
		return fmt.Errorf("unsupported containerd socket %s", l.Socket)
	}
	return nil
}

// Place makes the CNI plugins and bundled host binaries available in the
// extracted bundle. The plugin directory is Layout.CNIBinDir. The aux
// directory is Layout.BundledBin.
func Place(bundleDir string) (Layout, error) {
	layout, err := FromBundle(bundleDir)
	if err != nil {
		return Layout{}, err
	}
	if err := placeBundled(layout.CNIBinDir, layout.BundledBin); err != nil {
		return Layout{}, err
	}
	return layout, nil
}

func placeBundled(bin, aux string) error {
	if err := requireFile(filepath.Join(bin, "cni")); err != nil {
		return err
	}
	for _, name := range []string{"bridge", "host-local", "loopback", "portmap"} {
		if err := ensureLink(bin, name, "cni"); err != nil {
			return err
		}
	}
	if err := requireFile(filepath.Join(bin, "ip")); err != nil {
		return err
	}
	if err := requireFile(filepath.Join(bin, "busybox")); err != nil {
		return err
	}
	if err := ensureLink(bin, "modprobe", "busybox"); err != nil {
		return err
	}
	return alignIptables(aux)
}

// iptablesNames are the commands kube-proxy and the bridge plugin exec.
// They name one binary so a chain check and a rule restore use the same table.
var iptablesNames = []string{
	"iptables", "iptables-save", "iptables-restore",
	"ip6tables", "ip6tables-save", "ip6tables-restore",
}

// alignIptables points the iptables family at xtables-nft-multi.
// The k3s-root tree ships those names on that binary. A link that was
// aimed at xtables-legacy-multi is replaced.
func alignIptables(aux string) error {
	if err := requireFile(filepath.Join(aux, "xtables-nft-multi")); err != nil {
		return err
	}
	for _, name := range iptablesNames {
		if err := retargetLink(aux, name, "xtables-nft-multi"); err != nil {
			return err
		}
	}
	return nil
}

func retargetLink(dir, name, target string) error {
	path := filepath.Join(dir, name)
	got, err := os.Readlink(path)
	if err == nil && got == target {
		return nil
	}
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("bundled binary %s: %w", path, err)
	}
	if err == nil {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("place %s: %w", path, err)
		}
	}
	if err := os.Symlink(target, path); err != nil {
		return fmt.Errorf("place %s: %w", path, err)
	}
	return nil
}

func requireFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("bundled binary %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("bundled binary %s is not a file", path)
	}
	return nil
}

func ensureLink(dir, name, target string) error {
	path := filepath.Join(dir, name)
	got, err := os.Readlink(path)
	if err == nil {
		if got == target {
			return nil
		}
		return fmt.Errorf("%s points at %s", path, got)
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("bundled binary %s: %w", path, err)
	}
	if err := os.Symlink(target, path); err != nil {
		return fmt.Errorf("place %s: %w", path, err)
	}
	return nil
}
