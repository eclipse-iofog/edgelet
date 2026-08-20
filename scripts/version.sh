#!/bin/bash
# Edgelet embed pipeline version pins (RFC R16–R19, version-matrix.md).

VERSION_GOLANG="1.26.6"

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
  VERSION_CONTAINERD="v2.3.2-k3s2"
fi

VERSION_CNIPLUGINS="v1.9.1-k3s1"
VERSION_CRUN="1.28"
VERSION_PAUSE="portainer/pause:latest"

# k3s-root: static net aux (iptables, ip, busybox) — selective stage into embed tar.
VERSION_ROOT="v0.15.0"
K3S_ROOT_REPO="https://github.com/k3s-io/k3s-root/releases/download/${VERSION_ROOT}"
K3S_ROOT_TAR="k3s-root-${ARCH}.tar"

k3s_root_sha256() {
  case "${ARCH}" in
    amd64)
      echo "20066815d9941185fce3934cc3bae2fa3e2dbb46ca7e63462efb2ea59f1b15c4"
      ;;
    arm64)
      echo "4bdfc715dc8b5e2c4956f8686b895a56386f4cc6468215dcd22130a680650577"
      ;;
    arm)
      echo "2e43dac7750da52a756a9d4e8598d6e89937d565582660b26ca124bd9c8dbfaa"
      ;;
    riscv64)
      echo "0e79998acaa156059ba1ff676a4e5f290069e25f799de43ee016a0e780f9d4d3"
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
