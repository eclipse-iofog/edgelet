#!/bin/bash
# Run the thin binary on linux/amd64: server exits 0, port 6443 stays closed,
# and kubectl, ctr, and crictl reach the fat runtime.
set -euo pipefail

cd "$(dirname "$0")/.."

if [ "$(uname -s)" != "Linux" ] || [ "$(uname -m)" != "x86_64" ]; then
	echo "this check runs on linux/amd64" >&2
	exit 1
fi

thin="build/edgelet-kube"
fat="build/edgelet-kube-server"
if [ ! -x "$thin" ] || [ ! -x "$fat" ]; then
	echo "run scripts/build.sh first" >&2
	exit 1
fi

go version -m "$fat" | grep -q 'CGO_ENABLED=1'
go version -m "$thin" | grep -q 'CGO_ENABLED=0'

listen_6443() {
	awk 'NR > 1 && $4 == "0A" && $2 ~ /:192B$/' /proc/net/tcp /proc/net/tcp6 2>/dev/null || true
}

data=$(mktemp -d)
trap 'rm -rf "$data"' EXIT

out="$data/server.out"
if ! timeout 15s "$thin" server --data-dir "$data/work" >"$out"; then
	echo "server failed" >&2
	cat "$out" >&2
	exit 1
fi
grep -q 'edgelet-kube runtime server' "$out"
if listen_6443 | grep -q .; then
	echo "port 6443 is listening" >&2
	listen_6443 >&2
	exit 1
fi

mapfile -t extracted < <(find "$data/work/data" -type f -name edgelet-kube)
if [ "${#extracted[@]}" -ne 1 ]; then
	echo "expected one extracted runtime, found ${#extracted[@]}" >&2
	exit 1
fi
cmp -s "$fat" "${extracted[0]}"
[ -x "${extracted[0]}" ]

bindir=$(dirname "${extracted[0]}")
root=$(dirname "$bindir")
echo "bundle members:"
(
	cd "$root"
	find . -print | sed 's|^\./||' | LC_ALL=C sort
)
require_file() {
	if [ ! -f "$1" ] || [ -L "$1" ]; then
		echo "missing $1" >&2
		exit 1
	fi
}
require_link() {
	if [ ! -L "$1" ]; then
		echo "missing link $1" >&2
		exit 1
	fi
	got=$(readlink "$1")
	if [ "$got" != "$2" ]; then
		echo "$1 -> $got, want $2" >&2
		exit 1
	fi
}
require_file "$bindir/edgelet-kube"
require_file "$bindir/containerd-shim-runc-v2"
require_file "$bindir/crun"
require_file "$bindir/cni"
require_file "$bindir/ip"
require_file "$bindir/busybox"
require_file "$bindir/aux/xtables-nft-multi"
for name in iptables iptables-save iptables-restore ip6tables ip6tables-save ip6tables-restore; do
	require_link "$bindir/aux/$name" xtables-nft-multi
done
require_link "$bindir/modprobe" busybox
for name in bridge host-local loopback portmap; do
	require_link "$bindir/$name" cni
done
mapfile -t top < <(find "$bindir" -mindepth 1 -maxdepth 1 -printf '%f\n' | LC_ALL=C sort)
want_top="aux bridge busybox cni containerd-shim-runc-v2 crun edgelet-kube host-local ip loopback modprobe portmap"
got_top="${top[*]}"
if [ "$got_top" != "$want_top" ]; then
	echo "bin members: $got_top" >&2
	exit 1
fi
if find "$root" \( -name 'pause.tar' -o -name 'pause.tar.gz' -o -name 'oci-layout' -o -name 'index.json' -o -name 'manifest.json' -o -path '*/images/*' -o -path '*/blobs/*' -o -name 'flannel' \) | grep -q .; then
	echo "bundle contains an OCI image or flannel" >&2
	find "$root" \( -name 'pause.tar' -o -name 'pause.tar.gz' -o -name 'oci-layout' -o -name 'index.json' -o -name 'manifest.json' -o -path '*/images/*' -o -path '*/blobs/*' -o -name 'flannel' \) >&2
	exit 1
fi

k8s_version=$(awk '$1 == "kubernetes" { print $2; exit }' scripts/airgap/pins.txt)
kubectl_link="$data/kubectl"
ln -s "$(pwd)/$thin" "$kubectl_link"
got=$(timeout 30s "$kubectl_link" --data-dir "$data/work" version --client)
printf '%s\n' "$got" | grep -q "$k8s_version"
sub=$(timeout 30s "$thin" kubectl version --client --data-dir "$data/work")
printf '%s\n' "$sub" | grep -q "$k8s_version"

for name in ctr crictl; do
	link="$data/$name"
	ln -s "$(pwd)/$thin" "$link"
	got=$(timeout 15s "$link" --data-dir "$data/work")
	printf '%s\n' "$got" | grep -q "edgelet-kube runtime $name"
	sub=$(timeout 15s "$thin" "$name" --data-dir "$data/work")
	printf '%s\n' "$sub" | grep -q "edgelet-kube runtime $name"
done

empty="$data/empty"
mkdir -p "$empty"
if "$thin" --data-dir "$empty" >"$empty/out" 2>"$empty/err"; then
	echo "missing command should fail" >&2
	exit 1
fi
if [ -d "$empty/data" ]; then
	echo "a usage error extracted a bundle" >&2
	exit 1
fi
