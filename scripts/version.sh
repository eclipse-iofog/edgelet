#!/bin/bash
# Edgelet embed pipeline version pins (RFC R16–R19, version-matrix.md).

VERSION_GOLANG="1.26.11"

GO=${GO-go}
ARCH=${ARCH:-$("${GO}" env GOARCH)}
OS=${OS:-$("${GO}" env GOOS)}

get-module-version() {
  "${GO}" list -mod=readonly -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' "$1"
}

get-module-path() {
  "${GO}" list -mod=readonly -m -f '{{if .Replace}}{{.Replace.Path}}{{else}}{{.Path}}{{end}}' "$1"
}

PKG_CONTAINERD=$(get-module-path github.com/containerd/containerd/v2)
VERSION_CONTAINERD=$(get-module-version github.com/containerd/containerd/v2)
if [ -z "${VERSION_CONTAINERD}" ]; then
  VERSION_CONTAINERD="v2.3.7-k3s1"
fi

VERSION_CNIPLUGINS="v1.9.1-k3s1"
VERSION_CRUN="1.30.1"
# Multi-arch index digest of portainer/pause. build-embedded pulls this digest
# per arch, then retags the archive portainer/pause:latest for the CRI sandbox ref.
VERSION_PAUSE_REPO="portainer/pause"
VERSION_PAUSE_DIGEST="sha256:df6d2fb5cff97c4070a12953a34a56422879f1e02b43400f1caf1cbd530458b1"
VERSION_PAUSE="${VERSION_PAUSE_REPO}@${VERSION_PAUSE_DIGEST}"

# k3s-root: static net aux (iptables, ip, busybox) — selective stage into embed tar.
VERSION_ROOT="v0.15.4"
K3S_ROOT_REPO="https://github.com/k3s-io/k3s-root/releases/download/${VERSION_ROOT}"
K3S_ROOT_TAR="k3s-root-${ARCH}.tar"

k3s_root_sha256() {
  case "${ARCH}" in
    amd64)
      echo "658581f9dba924b24dcca14fcbb7237d69c9d3cd56995558d56efcd522263ea9"
      ;;
    arm64)
      echo "80b65fd619c8534a5cfe09310217a588e7cbb43f2f95b5f3dedcf2b39495dafc"
      ;;
    arm)
      echo "2a09ec8b4ab7a7fd41f9caa0b04a15cc631c2682f27f6f3605a77684717f3ac4"
      ;;
    riscv64)
      echo "fd5e0b38c0e13fba14ff45df2c0cb89fc8ae8b1c6f5a7480fd24f1d2b644b29e"
      ;;
    *)
      echo "[ERROR] unsupported ARCH for k3s-root: ${ARCH}" >&2
      return 1
      ;;
  esac
}

BINARY_POSTFIX=
if [ "${OS}" = windows ]; then
  BINARY_POSTFIX=.exe
fi
