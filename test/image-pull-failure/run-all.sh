#!/usr/bin/env bash
# Lima VM check: a missing image leaves a new workload queued and does not
# stop a controller container that is already running.
#
# Usage:
#   ./test/image-pull-failure/run-all.sh
#   ./test/image-pull-failure/run-all.sh --skip-setup --skip-build --skip-start

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
# shellcheck source=test/embedded/lib/log.sh
source "${REPO_ROOT}/test/embedded/lib/log.sh"
# shellcheck source=test/lima/lib/arch.sh
source "${REPO_ROOT}/test/lima/lib/arch.sh"

SKIP_SETUP=false
SKIP_BUILD=false
SKIP_START=false
VM_NAME="edgelet-engine-lifecycle"
TARGET_ARCH="$(lima_target_arch)"

for arg in "$@"; do
    case "${arg}" in
        --skip-setup) SKIP_SETUP=true ;;
        --skip-build) SKIP_BUILD=true ;;
        --skip-start) SKIP_START=true ;;
        --vm-name=*) VM_NAME="${arg#*=}" ;;
        --arch=*) TARGET_ARCH="${arg#*=}" ;;
        -h|--help)
            cat <<EOF
Usage: $0 [--skip-setup] [--skip-build] [--skip-start] [--vm-name=NAME] [--arch=arm64|amd64]

Installs edgelet with the docker engine on the Lima VM (this wipes that VM's
edgelet database) and checks image-pull failure behavior.
EOF
            exit 0
            ;;
        *) die "unknown argument ${arg}" ;;
    esac
done

# darwin_arm64_static_zlib prints a directory containing libz.a for the
# aarch64 cross linker. Homebrew's cross gcc does not ship static zlib.
darwin_arm64_static_zlib() {
    local prefix="${REPO_ROOT}/build/aarch64-zlib"
    local lib="${prefix}/lib/libz.a"
    if [[ -f "${lib}" ]]; then
        echo "${prefix}/lib"
        return
    fi
    command -v aarch64-linux-gnu-gcc >/dev/null || die "aarch64-linux-gnu-gcc required to build the linux fat runtime"
    local src="${REPO_ROOT}/build/src/zlib-1.3.1"
    mkdir -p "${REPO_ROOT}/build/src"
    if [[ ! -f "${src}/configure" ]]; then
        curl -fsSL "https://github.com/madler/zlib/releases/download/v1.3.1/zlib-1.3.1.tar.gz" | tar -xz -C "${REPO_ROOT}/build/src"
    fi
    (
        cd "${src}"
        make distclean >/dev/null 2>&1 || true
        CC=aarch64-linux-gnu-gcc CFLAGS="-O2" ./configure --static --prefix="${prefix}"
        # configure on Darwin selects Apple libtool, which cannot archive ELF objects.
        make -j"$(sysctl -n hw.ncpu)" AR=aarch64-linux-gnu-ar ARFLAGS=rc RANLIB=aarch64-linux-gnu-ranlib
        make install AR=aarch64-linux-gnu-ar ARFLAGS=rc RANLIB=aarch64-linux-gnu-ranlib
    )
    [[ -f "${lib}" ]] || die "static libz was not installed at ${lib}"
    echo "${prefix}/lib"
}

command -v limactl >/dev/null || die "limactl required"

if [[ "${SKIP_SETUP}" != true ]]; then
    "${REPO_ROOT}/test/engine-lifecycle/setup.sh"
fi

if [[ "${SKIP_BUILD}" != true ]]; then
    log_step "Rebuilding linux/${TARGET_ARCH} fat runtime and thin wrapper"
    link_env=()
    if [[ "$(uname -s)" == Darwin && "${TARGET_ARCH}" == arm64 ]]; then
        linker_path="$(darwin_arm64_static_zlib)"
        link_env=(LIBRARY_PATH="${linker_path}" CGO_LDFLAGS="-L${linker_path}")
    fi
    (
        cd "${REPO_ROOT}"
        chmod +x scripts/build-edgelet scripts/package-data scripts/binary_size_check.sh 2>/dev/null || true
        env "${link_env[@]}" ARCH="${TARGET_ARCH}" ./scripts/build-edgelet fat
        ARCH="${TARGET_ARCH}" ./scripts/package-data
        ARCH="${TARGET_ARCH}" EDGELET_CI_ARCHES="${TARGET_ARCH}" ./scripts/build-edgelet
        ARCH="${TARGET_ARCH}" ./scripts/binary_size_check.sh
    )
fi

EDGELET_BIN="${REPO_ROOT}/build/edgelet-linux-${TARGET_ARCH}"
[[ -f "${EDGELET_BIN}" ]] || die "missing ${EDGELET_BIN}"

if [[ "${SKIP_START}" != true ]]; then
    "${REPO_ROOT}/test/engine-lifecycle/vm-start.sh" --vm-name="${VM_NAME}"
fi

log_step "Installing edgelet with the docker engine on ${VM_NAME}"
"${REPO_ROOT}/test/engine-lifecycle/vm-install.sh" --vm-name="${VM_NAME}" --start-engine=docker --arch="${TARGET_ARCH}"

SSH_CONFIG="${HOME}/.lima/${VM_NAME}/ssh.config"
SSH_HOST="lima-${VM_NAME}"
REMOTE_DIR="/tmp/image-pull-failure"

ssh -F "${SSH_CONFIG}" "${SSH_HOST}" "rm -rf ${REMOTE_DIR} && mkdir -p ${REMOTE_DIR}/fixtures"
scp -F "${SSH_CONFIG}" -q \
    "${SCRIPT_DIR}/inside.sh" \
    "${SSH_HOST}:${REMOTE_DIR}/inside.sh"
scp -F "${SSH_CONFIG}" -q \
    "${SCRIPT_DIR}/fixtures/queued-missing.yaml" \
    "${SCRIPT_DIR}/fixtures/controlplane-cp.yaml" \
    "${SCRIPT_DIR}/fixtures/controlplane-missing.yaml" \
    "${SSH_HOST}:${REMOTE_DIR}/fixtures/"

log_step "Image pull failure checks on ${VM_NAME}"
limactl --tty=false shell "${VM_NAME}" -- sudo bash "${REMOTE_DIR}/inside.sh" "${REMOTE_DIR}"

log_success "Image pull failure Lima checks passed"
