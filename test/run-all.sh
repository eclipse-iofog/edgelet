#!/usr/bin/env bash
# Unified Lima / regression test orchestrator.
#
# Usage:
#   ./test/run-all.sh --suite=workload-continuity
#   ./test/run-all.sh --suite=embedded [--skip-build] [--skip-start]
#   ./test/run-all.sh --suite=embedded-cgroup-v1
#   ./test/run-all.sh --suite=engine-lifecycle
#   ./test/run-all.sh --suite=image-pull-failure
#   ./test/run-all.sh --suite=init
#   ./test/run-all.sh --suite=nested-docker
#   ./test/run-all.sh --suite=unit

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
# shellcheck source=test/lima/lib/log.sh
source "${REPO_ROOT}/test/lima/lib/log.sh"

SUITE=""
FORWARD_ARGS=()

for arg in "$@"; do
    case "${arg}" in
        --suite=*) SUITE="${arg#*=}" ;;
        -h|--help)
            cat <<EOF
Usage: $0 --suite=NAME [suite-specific options]

Suites:
  workload-continuity   docker/embedded restart gates (test/workload-continuity/run-all.sh)
  control-plane         ControlPlane apply + DNS (test/control-plane/run-all.sh)
  embedded              Full embedded matrix on iofog-test
  embedded-cgroup-v1    Hybrid cgroup v1 gate on iofog-test-v1 (optional)
  engine-lifecycle      Cold engine switch
  image-pull-failure    Missing image stays queued; running controller stays up
  init                  Init packaging smokes
  nested-docker         Build edgelet image + nested Docker smokes (Mac host)
  unit                  go test internal/cgroups + supervisor (short)

Additional flags are forwarded to the suite runner (e.g. --skip-build).
EOF
            exit 0
            ;;
        *) FORWARD_ARGS+=("${arg}") ;;
    esac
done

[[ -n "${SUITE}" ]] || die "Missing --suite= (see --help)"

run_suite() {
    case "${SUITE}" in
        workload-continuity)
            exec "${REPO_ROOT}/test/workload-continuity/run-all.sh" "${FORWARD_ARGS[@]}"
            ;;
        control-plane)
            exec "${REPO_ROOT}/test/control-plane/run-all.sh" "${FORWARD_ARGS[@]}"
            ;;
        embedded)
            exec "${REPO_ROOT}/test/embedded/run-all.sh" "${FORWARD_ARGS[@]}"
            ;;
        embedded-cgroup-v1)
            exec "${REPO_ROOT}/test/embedded/run-all-cgroup-v1.sh" "${FORWARD_ARGS[@]}"
            ;;
        engine-lifecycle)
            exec "${REPO_ROOT}/test/engine-lifecycle/run-all.sh" "${FORWARD_ARGS[@]}"
            ;;
        image-pull-failure)
            exec "${REPO_ROOT}/test/image-pull-failure/run-all.sh" "${FORWARD_ARGS[@]}"
            ;;
        init)
            exec "${REPO_ROOT}/test/init/run-all.sh" "${FORWARD_ARGS[@]}"
            ;;
        nested-docker)
            exec "${REPO_ROOT}/test/embedded/run-all-nested-docker.sh" "${FORWARD_ARGS[@]}"
            ;;
        unit)
            cd "${REPO_ROOT}"
            go test ./internal/cgroups/... ./internal/supervisor/... -count=1 -short
            ;;
        *)
            die "Unknown suite '${SUITE}' (see --help)"
            ;;
    esac
}

log_step "Running suite: ${SUITE}"
run_suite
