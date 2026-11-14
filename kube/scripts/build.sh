#!/bin/bash
# Build the thin edgelet-kube binary and the fat runtime it extracts.
# Thin is CGO_ENABLED=0. Fat is CGO_ENABLED=1.
# The zstd bundle holds the fat runtime and the aux binaries. It holds no OCI image.
# The airgap image archive is published beside this binary. It is not embedded here.
# On macOS the Linux binaries are built inside the embed image, which has the C toolchain.
set -euo pipefail

cd "$(dirname "$0")/.."
export GOWORK=off

if [ "$(uname -s)" = Darwin ]; then
	case "$(uname -m)" in
	arm64|aarch64) arch=arm64 ;;
	*) arch=amd64 ;;
	esac
	for arg in "$@"; do
		case "$arg" in
		--arch=*) arch="${arg#*=}" ;;
		*)
			echo "unknown argument: $arg" >&2
			exit 2
			;;
		esac
	done
	case "$arch" in
	amd64|arm64|arm|riscv64) ;;
	*)
		echo "unsupported architecture: $arch" >&2
		exit 2
		;;
	esac
	repo="$(cd .. && pwd)"
	uid="$(id -u)"
	gid="$(id -g)"
	echo "building linux/${arch} inside the embed image"
	# arm and riscv64 cross-compile in the host image. amd64 and arm64 run as that platform.
	if [ "$arch" = amd64 ] || [ "$arch" = arm64 ]; then
		docker build --platform "linux/${arch}" -f "${repo}/build/Dockerfile.embedded" -t edgelet-embed-ci "${repo}"
		docker run --platform "linux/${arch}" --rm \
			-v "${repo}:/src" -w /src \
			-e GOWORK=off -e ARCH="${arch}" \
			edgelet-embed-ci \
			bash kube/scripts/build.sh
		docker run --platform "linux/${arch}" --rm \
			-v "${repo}:/src" -w /src \
			edgelet-embed-ci \
			chown -R "${uid}:${gid}" kube/build kube/cmd/edgelet-kube/embed
	else
		docker build -f "${repo}/build/Dockerfile.embedded" -t edgelet-embed-ci "${repo}"
		docker run --rm \
			-v "${repo}:/src" -w /src \
			-e GOWORK=off -e ARCH="${arch}" \
			edgelet-embed-ci \
			bash kube/scripts/build.sh
		docker run --rm \
			-v "${repo}:/src" -w /src \
			edgelet-embed-ci \
			chown -R "${uid}:${gid}" kube/build kube/cmd/edgelet-kube/embed
	fi
	exit 0
fi

cc_for_arch() {
	host=$(uname -m)
	case "$1" in
	amd64)
		if [ "$host" = x86_64 ]; then
			echo "${CC:-gcc}"
		else
			echo "${CC:-x86_64-linux-gnu-gcc}"
		fi
		;;
	arm64)
		if [ "$host" = aarch64 ] || [ "$host" = arm64 ]; then
			echo "${CC:-gcc}"
		else
			echo "${CC:-aarch64-linux-gnu-gcc}"
		fi
		;;
	arm) echo "${CC:-arm-linux-gnueabihf-gcc}" ;;
	riscv64) echo "${CC:-riscv64-linux-gnu-gcc}" ;;
	*)
		echo "unsupported architecture: $1" >&2
		return 1
		;;
	esac
}

arch="${ARCH:-amd64}"
case "$arch" in
amd64|arm64|arm|riscv64) ;;
*)
	echo "unsupported architecture: $arch" >&2
	exit 2
	;;
esac
root="$(cd .. && pwd)"

bash scripts/bundle-stage.sh ensure "$root" "$arch"

mkdir -p build
k8s_version=$(awk '$1 == "kubernetes" { print $2; exit }' scripts/airgap/pins.txt)
k8s_rest=${k8s_version#v}
k8s_major=${k8s_rest%%.*}
k8s_minor=${k8s_rest#*.}
k8s_minor=${k8s_minor%%.*}
# -s -w omit the symbol table and DWARF info. The fat runtime is stripped
# before it is packed, so the thin binary does not carry them either.
version_ldflags="-X k8s.io/component-base/version.gitVersion=${k8s_version} -X k8s.io/component-base/version.gitMajor=${k8s_major} -X k8s.io/component-base/version.gitMinor=${k8s_minor} -X k8s.io/client-go/pkg/version.gitVersion=${k8s_version} -X k8s.io/client-go/pkg/version.gitMajor=${k8s_major} -X k8s.io/client-go/pkg/version.gitMinor=${k8s_minor}"
ldflags="-s -w ${version_ldflags}"
# Fat tags match the embed runtime. The thin launcher only drops CLI docs.
fat_tags="netcgo osusergo urfave_cli_no_docs sqlite_omit_load_extension apparmor seccomp"
thin_tags="urfave_cli_no_docs"
goarm=""
if [ "$arch" = arm ]; then
	goarm="${GOARM:-7}"
fi
goarm_env=()
if [ -n "$goarm" ]; then
	goarm_env=(GOARM="$goarm")
fi
cc=$(cc_for_arch "$arch") || exit 1
CGO_ENABLED=1 GOOS=linux GOARCH="$arch" "${goarm_env[@]}" CC="$cc" \
	go build -trimpath -tags "${fat_tags}" -ldflags "${ldflags}" -o build/edgelet-kube-server ./cmd/edgelet-kube-server

stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
bash scripts/bundle-stage.sh assemble "$stage" "$root" build/edgelet-kube-server
env -u GOOS -u GOARCH go run ./scripts/pack -stage "$stage" -out cmd/edgelet-kube/embed
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" "${goarm_env[@]}" go build -trimpath -tags "${thin_tags}" -ldflags "-s -w" -o build/edgelet-kube ./cmd/edgelet-kube
