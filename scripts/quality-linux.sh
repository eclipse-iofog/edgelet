#!/usr/bin/env bash
# Run lint, vulncheck, and security-code inside Linux Docker (macOS-friendly CI parity).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

GO_IMAGE="${GO_IMAGE:-golang:1.26.6}"
GOARCH="${GOARCH:-arm64}"
GOLANGCI_LINT_VERSION="${GOLANGCI_LINT_VERSION:-v2.12.2}"
GOVULNCHECK_VERSION="${GOVULNCHECK_VERSION:-v1.1.4}"

echo "=== quality-linux (linux/${GOARCH}) ==="

docker run --rm --platform "linux/${GOARCH}" \
	-v "${ROOT}:/src" -w /src "${GO_IMAGE}" bash -euxo pipefail -c "
		go version
		go mod download

		echo '=== go vet ==='
		go vet ./...

		curl -sSfL https://github.com/golangci/golangci-lint/releases/download/${GOLANGCI_LINT_VERSION}/golangci-lint-${GOLANGCI_LINT_VERSION#v}-linux-${GOARCH}.tar.gz \
			| tar xz -C /tmp
		GOLANGCI=/tmp/golangci-lint-${GOLANGCI_LINT_VERSION#v}-linux-${GOARCH}/golangci-lint

		echo '=== golangci-lint (full) ==='
		\${GOLANGCI} run --config .golangci.yaml --timeout=10m0s

		go install golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}
		chmod +x scripts/vulncheck.sh
		scripts/vulncheck.sh
		go mod verify

		go install github.com/securego/gosec/v2/cmd/gosec@latest
		gosec -exclude-dir=build ./cmd/... ./internal/... ./pkg/...
	"

echo "quality-linux (linux/${GOARCH}): vet, lint, vulncheck, and security-code passed"
