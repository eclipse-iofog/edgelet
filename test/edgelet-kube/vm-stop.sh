#!/usr/bin/env bash
# Stops the edgelet-kube-test Lima VM. --delete removes it.
#
# Usage:
#   ./test/edgelet-kube/vm-stop.sh [--vm-name=edgelet-kube-test] [--delete]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/log.sh
source "${SCRIPT_DIR}/lib/log.sh"

VM_NAME="edgelet-kube-test"
DELETE=false

for arg in "$@"; do
    case "${arg}" in
        --vm-name=*) VM_NAME="${arg#*=}" ;;
        --delete)    DELETE=true ;;
        --help|-h)
            sed -n '/^# Usage:/,/^[^#]/p' "$0" | grep '^#' | sed 's/^# \?//'
            exit 0
            ;;
        *) die "unknown option: ${arg}" ;;
    esac
done

refuse_agent_vm "${VM_NAME}"

if ! command -v limactl >/dev/null 2>&1; then
    log_warn "limactl not found — nothing to stop"
    exit 0
fi

status="$(limactl list --json 2>/dev/null \
    | jq -r "select(.name == \"${VM_NAME}\") | .status" 2>/dev/null \
    | head -1 || true)"

if [[ -z "${status}" ]]; then
    log_info "VM '${VM_NAME}' does not exist"
    exit 0
fi

if [[ "${status}" == "Running" ]]; then
    log_step "Stopping VM '${VM_NAME}'"
    limactl stop "${VM_NAME}"
    log_ok "VM stopped"
else
    log_info "VM '${VM_NAME}' is not running (status: ${status})"
fi

if [[ "${DELETE}" == "true" ]]; then
    log_step "Deleting VM '${VM_NAME}'"
    limactl delete "${VM_NAME}"
    log_ok "VM deleted"
fi
