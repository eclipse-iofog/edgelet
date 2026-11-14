#!/usr/bin/env bash
# Installs the thin binary in the Lima VM and starts edgelet-kube.service.
#
# Usage:
#   ./test/edgelet-kube/vm-install.sh [--vm-name=edgelet-kube-test] [--arch=arm64|amd64]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
# shellcheck source=lib/log.sh
source "${SCRIPT_DIR}/lib/log.sh"

VM_NAME="edgelet-kube-test"
TARGET_ARCH="$(go_arch "$(native_arch)")"

for arg in "$@"; do
    case "${arg}" in
        --vm-name=*) VM_NAME="${arg#*=}" ;;
        --arch=*)    TARGET_ARCH="$(go_arch "${arg#*=}")" ;;
        --help|-h)
            sed -n '/^# Usage:/,/^[^#]/p' "$0" | grep '^#' | sed 's/^# \?//'
            exit 0
            ;;
        *) die "unknown option: ${arg}" ;;
    esac
done

refuse_agent_vm "${VM_NAME}"

THIN="${REPO_ROOT}/kube/build/edgelet-kube"
FAT="${REPO_ROOT}/kube/build/edgelet-kube-server"
INSTALL="${REPO_ROOT}/kube/scripts/install.sh"

[[ -f "${THIN}" ]] || die "thin binary not found at ${THIN}. Run ./test/edgelet-kube/build.sh"
[[ -f "${FAT}" ]] || die "fat runtime not found at ${FAT}. Run ./test/edgelet-kube/build.sh"
[[ -f "${INSTALL}" ]] || die "install script not found at ${INSTALL}"
for bin in "${THIN}" "${FAT}"; do
    got="$(elf_go_arch "${bin}")"
    [[ "${got}" == "${TARGET_ARCH}" ]] || die "${bin} is linux/${got}, want linux/${TARGET_ARCH}"
done

# write_lima_node records the guest lima0 address as the node IP and on the
# serving certificate. That address is the one the Mac dials. eth0 is the
# user-mode address and owns the default route, so an unset node-ip publishes
# eth0 on LoadBalancer Services. An existing serving pair is removed so the
# next start issues a certificate that includes lima0.
write_lima_node() {
    local line ip elapsed=0
    while true; do
        line="$(limactl --tty=false shell "${VM_NAME}" -- ip -4 -o addr show dev lima0 2>/dev/null || true)"
        ip="$(printf '%s\n' "${line}" | awk 'NF { print $4; exit }' | cut -d/ -f1)"
        if [[ "${ip}" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
            break
        fi
        if (( elapsed >= 60 )); then
            die "guest lima0 has no IPv4 address"
        fi
        sleep 2
        elapsed=$((elapsed + 2))
    done
    log_info "Node IP and serving certificate will use guest lima0 ${ip}"
    limactl --tty=false shell "${VM_NAME}" -- sudo bash -c \
        "mkdir -p /etc/edgelet-kube && printf 'node-ip: %s\\ntls-san:\\n  - %s\\n' '${ip}' '${ip}' > /etc/edgelet-kube/config.yaml"
    limactl --tty=false shell "${VM_NAME}" -- sudo rm -f \
        /var/lib/edgelet-kube/server/tls/serving.crt \
        /var/lib/edgelet-kube/server/tls/serving.key
}

log_step "Installing edgelet-kube in VM '${VM_NAME}'"
write_lima_node
limactl --tty=false shell "${VM_NAME}" -- sudo sh "${INSTALL}" --bin-path="${THIN}"

log_info "Waiting for edgelet-kube.service to be active"
elapsed=0
until limactl --tty=false shell "${VM_NAME}" -- sudo systemctl is-active --quiet edgelet-kube >/dev/null 2>&1; do
    if (( elapsed >= 90 )); then
        limactl --tty=false shell "${VM_NAME}" -- sudo systemctl status edgelet-kube --no-pager -l || true
        die "edgelet-kube.service did not become active"
    fi
    sleep 3
    elapsed=$((elapsed + 3))
done
log_success "edgelet-kube.service is active in VM '${VM_NAME}'"
