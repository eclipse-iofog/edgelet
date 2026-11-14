#!/bin/bash
# Assemble bin/ for the zstd bundle from the fat runtime and the agent aux build.
# Usage: bundle-stage.sh ensure <repo-root> <arch>
#        bundle-stage.sh assemble <dest> <repo-root> <fat-runtime>
set -euo pipefail

usage() {
	echo "usage: bundle-stage.sh ensure <repo-root> <arch>" >&2
	echo "       bundle-stage.sh assemble <dest> <repo-root> <fat-runtime>" >&2
	exit 2
}

elf_machine() {
	od -An -t u2 -j 18 -N 2 "$1" | awk '{print $1}'
}

want_machine() {
	case "$1" in
	amd64) echo 62 ;;
	arm64) echo 183 ;;
	arm) echo 40 ;;
	riscv64) echo 243 ;;
	*)
		echo "unsupported architecture: $1" >&2
		return 1
		;;
	esac
}

matches_arch() {
	local path="$1"
	local machine="$2"
	local got
	[ -f "$path" ] && [ ! -L "$path" ] || return 1
	got=$(elf_machine "$path") || return 1
	[ "$got" = "$machine" ]
}

link_is() {
	[ -L "$1" ] && [ "$(readlink "$1")" = "$2" ]
}

bins_ready() {
	local machine="$1"
	local bin="$2"
	local name
	for name in containerd-shim-runc-v2 crun cni; do
		matches_arch "$bin/$name" "$machine" || return 1
	done
	for name in bridge host-local loopback portmap; do
		link_is "$bin/$name" cni || return 1
	done
}

aux_ready() {
	local machine="$1"
	local staged="$2"
	local name
	matches_arch "$staged/ip" "$machine" || return 1
	matches_arch "$staged/busybox" "$machine" || return 1
	matches_arch "$staged/aux/xtables-nft-multi" "$machine" || return 1
	for name in iptables iptables-save iptables-restore ip6tables ip6tables-save ip6tables-restore; do
		link_is "$staged/aux/$name" xtables-nft-multi || return 1
	done
	link_is "$staged/modprobe" busybox || return 1
}

align_iptables_nft() {
	local aux="$1/aux"
	local name
	[ -f "$aux/xtables-nft-multi" ] || return 1
	for name in iptables iptables-save iptables-restore ip6tables ip6tables-save ip6tables-restore; do
		rm -f "$aux/$name"
		ln -s xtables-nft-multi "$aux/$name"
	done
}

deps_present() {
	local pc
	command -v gcc >/dev/null 2>&1 || return 1
	command -v autoconf >/dev/null 2>&1 || return 1
	for pc in libseccomp libsystemd json-c; do
		pkg-config --exists "$pc" 2>/dev/null || return 1
	done
}

install_build_deps() {
	local root="$1"
	local arch="$2"
	if deps_present; then
		return 0
	fi
	if [ "$(id -u)" -eq 0 ]; then
		TARGETARCH="$arch" bash "$root/scripts/install-embed-build-deps"
		return 0
	fi
	if command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1; then
		sudo -n TARGETARCH="$arch" bash "$root/scripts/install-embed-build-deps"
		return 0
	fi
	echo "linux/$arch aux build needs gcc, autoconf, libseccomp, libsystemd, and json-c" >&2
	exit 1
}

ensure_aux() {
	local root="$1"
	local arch="$2"
	local machine bin staged
	machine=$(want_machine "$arch")
	bin="$root/build/bin"
	staged="$root/build/stage/bin"
	if ! bins_ready "$machine" "$bin"; then
		if [ "$(uname -s)" != "Linux" ]; then
			echo "linux/$arch shim, crun, and cni are not in $bin" >&2
			echo "The agent builds crun on Linux. Build those binaries for linux/$arch, then run this again." >&2
			exit 1
		fi
		install_build_deps "$root" "$arch"
		env -u GOOS -u GOARCH ARCH="$arch" bash "$root/scripts/download"
		if ! env -u GOOS -u GOARCH ARCH="$arch" bash "$root/scripts/build-embedded"; then
			if ! bins_ready "$machine" "$bin"; then
				echo "agent source-build did not produce linux/$arch shim, crun, and cni" >&2
				exit 1
			fi
			echo "shim, crun, and cni are ready; the image pull is not part of this bundle" >&2
		fi
		if ! bins_ready "$machine" "$bin"; then
			echo "agent source-build did not produce linux/$arch shim, crun, and cni" >&2
			exit 1
		fi
	fi
	if ! aux_ready "$machine" "$staged"; then
		mkdir -p "$staged"
		env -u GOOS -u GOARCH ARCH="$arch" bash "$root/scripts/download-root"
		env -u GOOS -u GOARCH ARCH="$arch" bash "$root/scripts/stage-root-aux"
		# The agent stage points iptables at xtables-legacy-multi and leaves
		# iptables-save and iptables-restore on xtables-nft-multi. kube-proxy
		# uses all three, so this bundle keeps the k3s-root nft links.
		align_iptables_nft "$staged" || true
		if ! aux_ready "$machine" "$staged"; then
			echo "agent source-build did not produce linux/$arch ip, busybox, and xtables-nft" >&2
			exit 1
		fi
	fi
}

assemble_stage() {
	local dest="$1"
	local root="$2"
	local fat="$3"
	local bin staged name
	if [ ! -s "$fat" ]; then
		echo "fat runtime is missing" >&2
		exit 1
	fi
	bin="$root/build/bin"
	staged="$root/build/stage/bin"
	mkdir -p "$dest/bin"
	cp "$fat" "$dest/bin/edgelet-kube"
	chmod 755 "$dest/bin/edgelet-kube"
	cp -a "$bin/containerd-shim-runc-v2" "$bin/crun" "$bin/cni" "$dest/bin/"
	for name in bridge host-local loopback portmap; do
		ln -s cni "$dest/bin/$name"
	done
	cp -a "$staged/aux" "$dest/bin/aux"
	cp -a "$staged/ip" "$staged/busybox" "$dest/bin/"
	ln -s busybox "$dest/bin/modprobe"
}

if [ "$#" -lt 1 ]; then
	usage
fi

case "$1" in
ensure)
	[ "$#" -eq 3 ] || usage
	ensure_aux "$2" "$3"
	;;
assemble)
	[ "$#" -eq 4 ] || usage
	assemble_stage "$2" "$3" "$4"
	;;
*)
	usage
	;;
esac
