package bundle

import (
	"archive/tar"
	"bytes"
	"cmp"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Member is one path stored in the zstd bundle.
type Member struct {
	Name string
	Link string
	Dir  bool
}

// Pack writes the stage directory as a zstd tar.
// The stage holds bin/edgelet-kube, the containerd shim, crun, the CNI
// multicall and its four symlinks, and the selective aux set.
// An OCI image is rejected.
func Pack(stageDir string) ([]byte, error) {
	entries, err := collect(stageDir)
	if err != nil {
		return nil, err
	}
	if err := validate(entries); err != nil {
		return nil, err
	}
	return archive(entries)
}

// Contents lists the members of a bundle written by Pack.
func Contents(packed []byte) ([]Member, error) {
	zr, err := zstd.NewReader(bytes.NewReader(packed))
	if err != nil {
		return nil, fmt.Errorf("zstd reader: %w", err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	var members []Member
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar read: %w", err)
		}
		members = append(members, Member{
			Name: strings.TrimSuffix(hdr.Name, "/"),
			Link: hdr.Linkname,
			Dir:  hdr.FileInfo().IsDir(),
		})
	}
	slices.SortFunc(members, func(a, b Member) int {
		return cmp.Compare(a.Name, b.Name)
	})
	return members, nil
}

type entry struct {
	name string
	path string
	mode os.FileMode
	link string
	size int64
}

func collect(stageDir string) ([]entry, error) {
	stageDir = filepath.Clean(stageDir)
	info, err := os.Stat(stageDir)
	if err != nil {
		return nil, fmt.Errorf("stage: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("stage %s is not a directory", stageDir)
	}
	var entries []entry
	err = filepath.WalkDir(stageDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == stageDir {
			return nil
		}
		rel, err := filepath.Rel(stageDir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		e := entry{name: rel, path: p, mode: info.Mode()}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return fmt.Errorf("read link %s: %w", rel, err)
			}
			e.link = target
		case info.IsDir():
		case info.Mode().IsRegular():
			e.size = info.Size()
		default:
			return fmt.Errorf("bundle member %s has an unsupported type", rel)
		}
		entries = append(entries, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(entries, func(a, b entry) int {
		return cmp.Compare(a.name, b.name)
	})
	return entries, nil
}

func validate(entries []entry) error {
	byName := make(map[string]entry, len(entries))
	for _, e := range entries {
		if !validBundlePath(e.name) {
			return fmt.Errorf("bundle path %s is invalid", e.name)
		}
		if imageMember(e.name) {
			return fmt.Errorf("bundle member %s is an OCI image", e.name)
		}
		if flannelMember(e.name) {
			return fmt.Errorf("bundle member %s is flannel", e.name)
		}
		if e.link != "" && linkEscapes(e.name, e.link) {
			return fmt.Errorf("bundle link %s -> %s leaves the bundle", e.name, e.link)
		}
		parts := strings.Split(e.name, "/")
		if parts[0] != "bin" {
			return fmt.Errorf("bundle member %s is outside bin", e.name)
		}
		if len(parts) == 2 && !allowedBinChild(parts[1]) {
			return fmt.Errorf("bundle member %s is not a runtime binary", e.name)
		}
		byName[e.name] = e
	}
	for _, name := range requiredFiles {
		e, ok := byName[name]
		if !ok || !e.mode.IsRegular() {
			return fmt.Errorf("bundle is missing %s", name)
		}
		if e.size == 0 {
			return fmt.Errorf("bundle member %s is empty", name)
		}
	}
	for _, link := range requiredLinks {
		e, ok := byName[link.name]
		if !ok || e.mode&os.ModeSymlink == 0 {
			return fmt.Errorf("bundle is missing %s", link.name)
		}
		if e.link != link.target {
			return fmt.Errorf("bundle link %s -> %s, want %s", link.name, e.link, link.target)
		}
	}
	return nil
}

func archive(entries []entry) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf,
		zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(16)),
		zstd.WithWindowSize(1<<25),
	)
	if err != nil {
		return nil, fmt.Errorf("zstd writer: %w", err)
	}
	tw := tar.NewWriter(zw)
	modTime := time.Unix(0, 0)
	for _, e := range entries {
		hdr := &tar.Header{
			Name:    e.name,
			ModTime: modTime,
			Mode:    int64(e.mode.Perm()),
		}
		switch {
		case e.mode&os.ModeSymlink != 0:
			hdr.Typeflag = tar.TypeSymlink
			hdr.Linkname = e.link
		case e.mode.IsDir():
			hdr.Typeflag = tar.TypeDir
			hdr.Mode = 0o755
		default:
			hdr.Typeflag = tar.TypeReg
			hdr.Size = e.size
			if hdr.Mode&0o111 == 0 {
				hdr.Mode = 0o755
			}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, fmt.Errorf("tar header %s: %w", e.name, err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		f, err := os.Open(e.path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.name, err)
		}
		n, copyErr := io.Copy(tw, f)
		closeErr := f.Close()
		if copyErr != nil {
			return nil, fmt.Errorf("tar write %s: %w", e.name, copyErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close %s: %w", e.name, closeErr)
		}
		if n != e.size {
			return nil, fmt.Errorf("wrote %d bytes to %s; expected %d", n, e.name, e.size)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("tar close: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("zstd close: %w", err)
	}
	return buf.Bytes(), nil
}

type linkSpec struct {
	name   string
	target string
}

var requiredFiles = []string{
	FatPath,
	"bin/containerd-shim-runc-v2",
	"bin/crun",
	"bin/cni",
	"bin/aux/xtables-nft-multi",
	"bin/ip",
	"bin/busybox",
}

var requiredLinks = []linkSpec{
	{name: "bin/bridge", target: "cni"},
	{name: "bin/host-local", target: "cni"},
	{name: "bin/loopback", target: "cni"},
	{name: "bin/portmap", target: "cni"},
	{name: "bin/aux/iptables", target: "xtables-nft-multi"},
	{name: "bin/aux/iptables-save", target: "xtables-nft-multi"},
	{name: "bin/aux/iptables-restore", target: "xtables-nft-multi"},
	{name: "bin/aux/ip6tables", target: "xtables-nft-multi"},
	{name: "bin/aux/ip6tables-save", target: "xtables-nft-multi"},
	{name: "bin/aux/ip6tables-restore", target: "xtables-nft-multi"},
	{name: "bin/modprobe", target: "busybox"},
}

func allowedBinChild(name string) bool {
	switch name {
	case "edgelet-kube", "containerd-shim-runc-v2", "crun", "cni",
		"bridge", "host-local", "loopback", "portmap",
		"aux", "ip", "busybox", "modprobe":
		return true
	default:
		return false
	}
}

func validBundlePath(name string) bool {
	if name == "" || path.IsAbs(name) || strings.Contains(name, "\\") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func imageMember(name string) bool {
	base := path.Base(name)
	switch base {
	case "oci-layout", "index.json", "manifest.json", "pause.tar", "pause.tar.gz":
		return true
	}
	if strings.HasSuffix(base, ".tar") || strings.HasSuffix(base, ".tar.gz") || strings.HasSuffix(base, ".tar.zst") {
		return true
	}
	if name == "images" || strings.HasPrefix(name, "images/") || strings.Contains(name, "/images/") {
		return true
	}
	if name == "blobs" || strings.HasPrefix(name, "blobs/") || strings.Contains(name, "/blobs/") {
		return true
	}
	return false
}

func flannelMember(name string) bool {
	for _, part := range strings.Split(name, "/") {
		if part == "flannel" {
			return true
		}
	}
	return false
}

func linkEscapes(name, target string) bool {
	if target == "" || path.IsAbs(target) || strings.Contains(target, "\\") {
		return true
	}
	cleaned := path.Clean(path.Join(path.Dir(name), target))
	return cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/")
}
