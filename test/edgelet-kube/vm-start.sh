#!/usr/bin/env bash
# Creates and starts the edgelet-kube-test Lima VM.
#
# Usage:
#   ./test/edgelet-kube/vm-start.sh [--vm-name=edgelet-kube-test] [--arch=arm64|amd64] [--timeout=300]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/log.sh
source "${SCRIPT_DIR}/lib/log.sh"

VM_NAME="edgelet-kube-test"
TIMEOUT=300
TARGET_ARCH="$(go_arch "$(native_arch)")"
LIMA_YAML="${SCRIPT_DIR}/lima.yaml"

for arg in "$@"; do
    case "${arg}" in
        --vm-name=*) VM_NAME="${arg#*=}" ;;
        --arch=*)    TARGET_ARCH="$(go_arch "${arg#*=}")" ;;
        --timeout=*) TIMEOUT="${arg#*=}" ;;
        --help|-h)
            sed -n '/^# Usage:/,/^[^#]/p' "$0" | grep '^#' | sed 's/^# \?//'
            exit 0
            ;;
        *) die "unknown option: ${arg}" ;;
    esac
done

refuse_agent_vm "${VM_NAME}"
command -v limactl >/dev/null 2>&1 || die "limactl not found. Run ./test/edgelet-kube/setup.sh first."
command -v jq >/dev/null 2>&1 || die "jq not found. Run ./test/edgelet-kube/setup.sh first."

LIMA_ARCH="$(lima_arch "${TARGET_ARCH}")"
HOST_ARCH="$(go_arch "$(native_arch)")"
VM_TYPE="vz"
if [[ "${TARGET_ARCH}" != "${HOST_ARCH}" ]]; then
    VM_TYPE="qemu"
    log_warn "linux/${TARGET_ARCH} does not match the host. The guest will use qemu."
fi

vm_status() {
    limactl list --json 2>/dev/null \
        | jq -r "select(.name == \"${VM_NAME}\") | .status" 2>/dev/null \
        | head -1 || true
}

vm_arch() {
    limactl list --json 2>/dev/null \
        | jq -r "select(.name == \"${VM_NAME}\") | .arch" 2>/dev/null \
        | head -1 || true
}

status="$(vm_status)"
if [[ -n "${status}" ]]; then
    got="$(vm_arch)"
    if [[ -n "${got}" && "${got}" != "${LIMA_ARCH}" ]]; then
        die "VM '${VM_NAME}' is ${got}, want ${LIMA_ARCH}. Delete it with ./test/edgelet-kube/vm-stop.sh --delete and re-run."
    fi
    log_info "VM '${VM_NAME}' already exists (status: ${status})"
else
    log_step "Creating Lima VM '${VM_NAME}' (${LIMA_ARCH}, ${VM_TYPE})"
    limactl --tty=false create \
        --name="${VM_NAME}" \
        --arch="${LIMA_ARCH}" \
        --vm-type="${VM_TYPE}" \
        "${LIMA_YAML}"
    log_ok "VM '${VM_NAME}' created"
fi

status="$(vm_status)"
if [[ "${status}" == "Running" ]]; then
    log_info "VM '${VM_NAME}' is already running"
else
    log_step "Starting VM '${VM_NAME}'"
    limactl --tty=false start --timeout=1200s "${VM_NAME}" \
        || die "Could not start VM '${VM_NAME}'"
fi

log_step "Waiting for SSH (timeout: ${TIMEOUT}s)"
elapsed=0
until limactl --tty=false shell "${VM_NAME}" -- echo ready >/dev/null 2>&1; do
    if (( elapsed >= TIMEOUT )); then
        die "VM '${VM_NAME}' did not accept SSH within ${TIMEOUT}s"
    fi
    sleep 5
    elapsed=$((elapsed + 5))
done

log_info "Waiting for provisioning to finish"
elapsed=0
until limactl --tty=false shell "${VM_NAME}" -- test -f /var/tmp/edgelet-kube-vm-ready >/dev/null 2>&1; do
    if (( elapsed >= TIMEOUT )); then
        die "VM '${VM_NAME}' did not finish provisioning within ${TIMEOUT}s"
    fi
    sleep 5
    elapsed=$((elapsed + 5))
done

limactl --tty=false shell "${VM_NAME}" -- sudo modprobe overlay >/dev/null 2>&1 || true
limactl --tty=false shell "${VM_NAME}" -- sudo modprobe br_netfilter >/dev/null 2>&1 || true
cgroup="$(limactl --tty=false shell "${VM_NAME}" -- stat -fc %T /sys/fs/cgroup 2>/dev/null || echo unknown)"
log_info "cgroup filesystem: ${cgroup}"
log_success "VM '${VM_NAME}' is ready"
