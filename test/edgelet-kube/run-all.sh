#!/usr/bin/env bash
# Runs the edgelet-kube Lima suite: setup, build, start, install, test.
#
# Usage:
#   ./test/edgelet-kube/run-all.sh [options]
#
# Options:
#   --skip-setup      Skip host prerequisite checks
#   --skip-build      Reuse kube/build/edgelet-kube and kube/build/edgelet-kube-server
#   --skip-start      Skip VM creation (the VM must already be running)
#   --skip-stop       Keep the VM running after the checks (default)
#   --delete-vm       Stop and delete the VM after the checks
#   --vm-name=NAME    Lima VM name (default: edgelet-kube-test)
#   --arch=ARCH       Linux arch: arm64 | amd64 (default: the host arch)
#   --timeout=N       Seconds to wait for the VM (default: 300)
#   --ready-timeout=N Seconds to wait for the conflist and a Ready node (default: 1200)
#   --ci              On failure, delete the VM when --delete-vm was set
#
# Exit code:
#   0  The checks passed
#   1  Setup, build, install, or a check failed

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
# shellcheck source=lib/log.sh
source "${SCRIPT_DIR}/lib/log.sh"

SKIP_SETUP=false
SKIP_BUILD=false
SKIP_START=false
SKIP_STOP=true
DELETE_VM=false
VM_NAME="edgelet-kube-test"
TIMEOUT=300
READY_TIMEOUT=1200
CI_MODE=false
TARGET_ARCH="$(go_arch "$(native_arch)")"

for arg in "$@"; do
    case "${arg}" in
        --skip-setup)      SKIP_SETUP=true ;;
        --skip-build)      SKIP_BUILD=true ;;
        --skip-start)      SKIP_START=true ;;
        --skip-stop)       SKIP_STOP=true ;;
        --delete-vm)       DELETE_VM=true; SKIP_STOP=false ;;
        --vm-name=*)       VM_NAME="${arg#*=}" ;;
        --arch=*)          TARGET_ARCH="$(go_arch "${arg#*=}")" ;;
        --timeout=*)       TIMEOUT="${arg#*=}" ;;
        --ready-timeout=*) READY_TIMEOUT="${arg#*=}" ;;
        --ci)              CI_MODE=true ;;
        --help|-h)
            sed -n '/^# Usage:/,/^[^#]/p' "$0" | grep '^#' | sed 's/^# \?//'
            exit 0
            ;;
        *) log_warn "Unknown option: ${arg}" ;;
    esac
done

refuse_agent_vm "${VM_NAME}"

HOST_ARCH="$(go_arch "$(native_arch)")"

echo ""
echo -e "${_BOLD}======================================================================${_RESET}"
echo -e "${_BOLD}  edgelet-kube Lima suite${_RESET}"
echo -e "${_BOLD}  Host: $(uname -s)/${HOST_ARCH}   Target: linux/${TARGET_ARCH}   VM: ${VM_NAME}${_RESET}"
echo -e "${_BOLD}======================================================================${_RESET}"
echo ""

START_TIME=$(date +%s)
OVERALL_STATUS=0

cleanup() {
    local exit_code=$?
    if [[ "${exit_code}" -ne 0 && "${CI_MODE}" == "true" && "${DELETE_VM}" == "true" ]]; then
        log_warn "Cleaning up VM '${VM_NAME}' after failure"
        "${SCRIPT_DIR}/vm-stop.sh" --vm-name="${VM_NAME}" --delete 2>/dev/null || true
    fi
}
trap cleanup EXIT

require_binaries() {
    local thin="${REPO_ROOT}/kube/build/edgelet-kube"
    local fat="${REPO_ROOT}/kube/build/edgelet-kube-server"
    local bin got
    [[ -f "${thin}" && -f "${fat}" ]] || \
        die "No pre-built binaries in kube/build. Remove --skip-build to build."
    for bin in "${thin}" "${fat}"; do
        got="$(elf_go_arch "${bin}")"
        [[ "${got}" == "${TARGET_ARCH}" ]] || \
            die "${bin} is linux/${got}, want linux/${TARGET_ARCH}. Remove --skip-build to rebuild."
    done
}

if [[ "${SKIP_SETUP}" == "false" ]]; then
    log_step "Step 1/5: Host prerequisites"
    "${SCRIPT_DIR}/setup.sh"
else
    log_info "Skipping setup (--skip-setup)"
fi

if [[ "${SKIP_BUILD}" == "false" ]]; then
    log_step "Step 2/5: Building linux/${TARGET_ARCH} binaries"
    "${SCRIPT_DIR}/build.sh" --arch="${TARGET_ARCH}"
else
    log_info "Skipping build (--skip-build)"
    require_binaries
fi

if [[ "${SKIP_START}" == "false" ]]; then
    log_step "Step 3/5: Starting Lima VM '${VM_NAME}'"
    "${SCRIPT_DIR}/vm-start.sh" --vm-name="${VM_NAME}" --arch="${TARGET_ARCH}" --timeout="${TIMEOUT}"
else
    log_info "Skipping VM start (--skip-start)"
fi

log_step "Step 4/5: Installing edgelet-kube in the VM"
"${SCRIPT_DIR}/vm-install.sh" --vm-name="${VM_NAME}" --arch="${TARGET_ARCH}"

log_step "Step 5/5: Checking the node"
set +e
"${SCRIPT_DIR}/vm-test.sh" --vm-name="${VM_NAME}" --ready-timeout="${READY_TIMEOUT}"
TEST_EXIT=$?
set -e
if [[ "${TEST_EXIT}" -ne 0 ]]; then
    OVERALL_STATUS=1
fi

if [[ "${DELETE_VM}" == "true" ]]; then
    log_step "Tearing down VM '${VM_NAME}'"
    "${SCRIPT_DIR}/vm-stop.sh" --vm-name="${VM_NAME}" --delete
elif [[ "${SKIP_STOP}" == "false" ]]; then
    "${SCRIPT_DIR}/vm-stop.sh" --vm-name="${VM_NAME}"
else
    echo ""
    log_info "VM '${VM_NAME}' is still running."
    log_info "  Re-run checks: ./test/edgelet-kube/vm-test.sh --vm-name=${VM_NAME}"
    log_info "  Open a shell:  limactl shell ${VM_NAME}"
    log_info "  Stop the VM:   ./test/edgelet-kube/vm-stop.sh --vm-name=${VM_NAME}"
    log_info "  Delete the VM: ./test/edgelet-kube/vm-stop.sh --vm-name=${VM_NAME} --delete"
fi

END_TIME=$(date +%s)
ELAPSED=$((END_TIME - START_TIME))

echo ""
echo -e "${_BOLD}======================================================================"
if [[ "${OVERALL_STATUS}" -eq 0 ]]; then
    echo -e "${_GREEN}  ALL CHECKS PASSED${_RESET}${_BOLD}  (${ELAPSED}s)"
else
    echo -e "${_RED}  CHECKS FAILED${_RESET}${_BOLD}  (${ELAPSED}s)"
fi
echo -e "${_BOLD}======================================================================${_RESET}"
echo ""

exit "${OVERALL_STATUS}"
