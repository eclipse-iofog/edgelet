package main

import (
	"fmt"
	"net/url"
	"sort"
)

// embedComponents are the binaries and images shipped inside the Linux zstd
// bundle. They are not Go module requirements. Versions and k3s-root digests
// come from scripts/version.sh.
func embedComponents(pins Pins, containerdPath, containerdVersion, gpl string) []Component {
	var out []Component
	var parents []string
	for _, arch := range []string{"amd64", "arm64", "arm", "riscv64"} {
		sum := pins.K3sSHA[arch]
		ref := fmt.Sprintf("pkg:generic/k3s-root@%s?arch=%s", url.QueryEscape(pins.K3sRoot), arch)
		parents = append(parents, ref)
		out = append(out, Component{
			Ref:      ref,
			Type:     "file",
			Name:     "k3s-root (" + arch + ")",
			Version:  pins.K3sRoot,
			PURL:     ref,
			Scope:    scopeRequired,
			License:  "NOASSERTION",
			Closures: []string{"linux"},
			Hashes:   []Hash{{Alg: "SHA-256", Content: sum}},
			Props: []Prop{
				prop("edgelet:arch", arch),
				prop("edgelet:download", "https://github.com/k3s-io/k3s-root/releases/download/"+pins.K3sRoot+"/k3s-root-"+arch+".tar"),
			},
			Relation:    relDepends,
			LicenseText: k3sRootLicenseNote,
			Note:        "Upstream tarball pin. The embed stages bin/busybox, bin/ip, and bin/aux from this archive. The hash is of the tarball, not of the staged subset. " + k3sRootLicenseNote,
		})
	}
	out = append(out,
		containedProject("busybox", "GPL-2.0-only", gpl, parents, "bin/busybox. k3s-root v"+trimV(pins.K3sRoot)+" enables BR2_PACKAGE_BUSYBOX and does not enable a separate kmod package."),
		containedProject("iproute2", "GPL-2.0-only", gpl, parents, "bin/ip. k3s-root enables BR2_PACKAGE_IPROUTE2."),
		containedProject("iptables", "GPL-2.0-or-later", gpl, parents, "bin/aux iptables, xtables, and arptables names. k3s-root enables BR2_PACKAGE_IPTABLES. arptables is not a separate buildroot package in this pin."),
		containedProject("nftables", "GPL-2.0-only", gpl, parents, "bin/aux/nft. k3s-root enables BR2_PACKAGE_NFTABLES."),
		containedProject("ebtables", "GPL-2.0-only", gpl, parents, "bin/aux/ebtables*. k3s-root enables BR2_PACKAGE_EBTABLES."),
		containedProject("util-linux", "GPL-2.0-or-later", gpl, parents, "bin/aux/mount and other util-linux binaries staged with bin/aux. k3s-root enables BR2_PACKAGE_UTIL_LINUX_BINARIES."),
	)
	shimRef := golangPURL(containerdPath, containerdVersion) + "#cmd/containerd-shim-runc-v2"
	out = append(out, Component{
		Ref:      shimRef,
		Type:     "application",
		Name:     "containerd-shim-runc-v2",
		Version:  containerdVersion,
		PURL:     shimRef,
		Scope:    scopeRequired,
		License:  "Apache-2.0",
		Closures: []string{"linux"},
		Relation: relDepends,
		Note:     "Separate ELF built from the same containerd module that is linked into the fat runtime. It is not a second copy of that module's version.",
	})
	out = append(out, Component{
		Ref:         "pkg:github/containers/crun@" + pins.Crun,
		Type:        "application",
		Name:        "crun",
		Version:     pins.Crun,
		PURL:        "pkg:github/containers/crun@" + pins.Crun,
		Scope:       scopeRequired,
		License:     "GPL-2.0-or-later",
		LicenseText: gpl,
		Closures:    []string{"linux"},
		Relation:    relDepends,
		Props:       []Prop{prop("edgelet:source", "https://github.com/containers/crun/tree/"+pins.Crun)},
		Note:        "Source of the static crun ELF in the embed bundle.",
	})
	out = append(out, Component{
		Ref:      "pkg:github/rancher/plugins@" + pins.CNI,
		Type:     "application",
		Name:     "cni-plugins",
		Version:  pins.CNI,
		PURL:     "pkg:github/rancher/plugins@" + pins.CNI,
		Scope:    scopeRequired,
		License:  "Apache-2.0",
		Closures: []string{"linux"},
		Relation: relDepends,
		Note:     "Multicall binary plus bridge, host-local, loopback, and portmap symlinks.",
	})
	out = append(out, Component{
		Ref:      pins.pausePURL(),
		Type:     "container",
		Name:     pins.PauseRepo,
		Version:  pins.PauseDigest,
		PURL:     pins.pausePURL(),
		Scope:    scopeRequired,
		License:  "Apache-2.0",
		Closures: []string{"linux"},
		Hashes:   []Hash{{Alg: "SHA-256", Content: pins.PauseDigest[len("sha256:"):]}},
		Relation: relDepends,
		Note:     "Multi-arch index digest. Each Linux build pulls this digest for its architecture and retags the archive portainer/pause:latest. portainer/kubesolo builds the image by compiling kubernetes/kubernetes build/pause/linux/pause.c. That source is Apache-2.0. The workflow checks out Kubernetes master and does not pin a commit. The MIT license of the kubesolo repository covers that repository, not this image.",
	})
	return out
}

func containedProject(name, license, text string, parents []string, note string) Component {
	ref := "pkg:generic/" + name + "?vendored-by=k3s-root"
	return Component{
		Ref:         ref,
		Type:        "application",
		Name:        name,
		PURL:        ref,
		Scope:       scopeRequired,
		License:     license,
		LicenseText: text,
		Closures:    []string{"linux"},
		Relation:    relContains,
		ContainedBy: parents[0],
		Props:       parentProps(parents),
		Note:        note + " Upstream project version is whatever k3s-root's buildroot config selected; it is not pinned separately.",
	}
}

func parentProps(parents []string) []Prop {
	var out []Prop
	for _, p := range parents {
		out = append(out, prop("edgelet:contained-by", p))
	}
	return out
}

func trimV(v string) string {
	if len(v) > 0 && v[0] == 'v' {
		return v[1:]
	}
	return v
}

const k3sRootLicenseNote = "The k3s-root source repository at this tag is Apache-2.0 (https://github.com/k3s-io/k3s-root). That license covers the build scripts and Buildroot config. It does not license the programs inside the tarball. Those programs are the contained GPL components."

func sortComponents(comps []Component) {
	sort.Slice(comps, func(i, j int) bool {
		return comps[i].Ref < comps[j].Ref
	})
	for i := range comps {
		sort.Strings(comps[i].Closures)
		sort.Slice(comps[i].Props, func(a, b int) bool {
			if comps[i].Props[a].Name == comps[i].Props[b].Name {
				return comps[i].Props[a].Value < comps[i].Props[b].Value
			}
			return comps[i].Props[a].Name < comps[i].Props[b].Name
		})
	}
}
