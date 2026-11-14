#!/bin/bash
# Plan 6 A1: ensure go.mod replace resolves containerd to the k3s fork (RFC R2).
set -e

cd "$(dirname "$0")/.."

GO=${GO-go}
MODULE=github.com/containerd/containerd/v2
WANT_PATH=github.com/k3s-io/containerd/v2
WANT_VERSION=v2.3.7-k3s1

resolved_path=$("${GO}" list -mod=readonly -m -f '{{if .Replace}}{{.Replace.Path}}{{else}}{{.Path}}{{end}}' "${MODULE}")
resolved_version=$("${GO}" list -mod=readonly -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' "${MODULE}")

if [ "${resolved_path}" != "${WANT_PATH}" ] || [ "${resolved_version}" != "${WANT_VERSION}" ]; then
  echo "ERROR: ${MODULE} must resolve to ${WANT_PATH} ${WANT_VERSION}" >&2
  echo "  got: ${resolved_path} ${resolved_version}" >&2
  echo "  fix go.mod replace and run: go mod tidy" >&2
  exit 1
fi

echo "containerd fork OK: ${resolved_path} ${resolved_version}"
