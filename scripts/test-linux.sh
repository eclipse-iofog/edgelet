#!/usr/bin/env bash
# Run make test-unit inside Linux Docker (macOS-friendly parity with ci.yml Test job),
# plus the non-cgo Linux pass from scripts/ci. The race detector needs cgo, so that
# pass is skipped when TEST_FLAGS contains -race.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

GO_IMAGE="${GO_IMAGE:-golang:1.26.6}"
GOARCH="${GOARCH:-$(go env GOARCH)}"
TEST_FLAGS="${TEST_FLAGS:-}"
RUN_NOCGO=1

if [[ "${TEST_FLAGS}" == *"-race"* ]]; then
	RUN_NOCGO=0
	echo "=== test-linux (linux/${GOARCH}, race detector; TEST_FLAGS=${TEST_FLAGS}) ==="
else
	echo "=== test-linux (linux/${GOARCH}, test-unit plus non-cgo) ==="
fi

docker run --rm --platform "linux/${GOARCH}" \
	-e "TEST_FLAGS=${TEST_FLAGS}" \
	-e "EDGELET_RUN_NOCGO=${RUN_NOCGO}" \
	-v "${ROOT}:/src" -w /src "${GO_IMAGE}" bash -euxo pipefail -c "
		go version
		go mod download
		export GO111MODULE=on
		unset GOPATH
		_TEST_PKGS='./cmd/... ./internal/... ./pkg/... ./test/...'
		go test \${TEST_FLAGS} -v -short -tags '!linux' \${_TEST_PKGS}
		go test \${TEST_FLAGS} -v -short -tags linux \${_TEST_PKGS}
		CGO_ENABLED=1 go test \${TEST_FLAGS} -v -short -tags 'linux,cgo' \${_TEST_PKGS}
		if [ \"\${EDGELET_RUN_NOCGO}\" = 1 ]; then
			CGO_ENABLED=0 go test \${TEST_FLAGS} -v -short -tags 'linux,!cgo' \${_TEST_PKGS}
		fi
	"

if [[ "${TEST_FLAGS}" == *"-race"* ]]; then
	echo "test-linux (linux/${GOARCH}): passed (race detector)"
else
	echo "test-linux (linux/${GOARCH}): passed (test-unit plus non-cgo)"
fi
