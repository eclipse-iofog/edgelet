#!/usr/bin/env bash
# Vet and lint the kube module.
# On macOS the checks run in the Go Linux image. On Linux they run on the host.
set -euo pipefail

KUBE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ROOT="$(cd "${KUBE}/.." && pwd)"

GO_IMAGE="${GO_IMAGE:-golang:1.26.11}"
GOLANGCI_LINT_VERSION="${GOLANGCI_LINT_VERSION:-v2.12.2}"

run_checks() {
	cd "${KUBE}"
	export GOWORK=off
	local arch ver golangci
	arch="$(go env GOARCH)"
	ver="${GOLANGCI_LINT_VERSION#v}"
	echo "=== quality-linux-kube (linux/${arch}) ==="
	go version
	go mod download

	echo "=== go vet ==="
	go vet ./...

	dest="$(mktemp -d)"
	trap 'rm -rf "${dest}"' EXIT
	curl -sSfL "https://github.com/golangci/golangci-lint/releases/download/${GOLANGCI_LINT_VERSION}/golangci-lint-${ver}-linux-${arch}.tar.gz" \
		| tar xz -C "${dest}"
	golangci="${dest}/golangci-lint-${ver}-linux-${arch}/golangci-lint"

	echo "=== golangci-lint ==="
	"${golangci}" run --config .golangci.yaml --timeout=10m0s
	echo "quality-linux-kube (linux/${arch}): vet and lint passed"
}

case "$(uname -s)" in
Darwin)
	if [ -z "${GOARCH:-}" ]; then
		GOARCH="$(go env GOARCH)"
	fi
	modcache="${QUALITY_MODCACHE:-${HOME}/.cache/edgelet-kube/mod}"
	buildcache="${QUALITY_GOCACHE:-${HOME}/.cache/edgelet-kube/build}"
	mkdir -p "${modcache}" "${buildcache}"
	docker run --rm --platform "linux/${GOARCH}" \
		-e GOWORK=off \
		-e GOLANGCI_LINT_VERSION="${GOLANGCI_LINT_VERSION}" \
		-v "${ROOT}:/src" -w /src/kube \
		-v "${modcache}:/go/pkg/mod" \
		-v "${buildcache}:/root/.cache/go-build" \
		"${GO_IMAGE}" \
		bash /src/kube/scripts/quality-linux.sh
	;;
Linux)
	run_checks
	;;
*)
	echo "quality-linux-kube supports macOS and Linux" >&2
	exit 1
	;;
esac
