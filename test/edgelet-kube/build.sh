#!/usr/bin/env bash
# Builds the thin edgelet-kube binary and the fat runtime for the Lima guest.
# On macOS the Linux build runs inside the embed image.
#
# Usage:
#   ./test/edgelet-kube/build.sh [--arch=arm64|amd64]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
# shellcheck source=lib/log.sh
source "${SCRIPT_DIR}/lib/log.sh"

TARGET_ARCH="$(go_arch "$(native_arch)")"
for arg in "$@"; do
    case "${arg}" in
        --arch=*) TARGET_ARCH="$(go_arch "${arg#*=}")" ;;
        --help|-h)
            sed -n '/^# Usage:/,/^[^#]/p' "$0" | grep '^#' | sed 's/^# \?//'
            exit 0
            ;;
        *) die "unknown option: ${arg}" ;;
    esac
done

THIN="${REPO_ROOT}/kube/build/edgelet-kube"
FAT="${REPO_ROOT}/kube/build/edgelet-kube-server"

log_step "Building edgelet-kube for linux/${TARGET_ARCH}"
if [[ "$(uname -s)" == "Darwin" ]]; then
    "${REPO_ROOT}/kube/scripts/build.sh" --arch="${TARGET_ARCH}"
else
    ARCH="${TARGET_ARCH}" "${REPO_ROOT}/kube/scripts/build.sh"
fi

[[ -f "${THIN}" && -f "${FAT}" ]] || die "build did not write ${THIN} and ${FAT}"
for bin in "${THIN}" "${FAT}"; do
    got="$(elf_go_arch "${bin}")"
    [[ "${got}" == "${TARGET_ARCH}" ]] || die "${bin} is linux/${got}, want linux/${TARGET_ARCH}"
done
log_ok "linux/${TARGET_ARCH} thin binary and fat runtime are in kube/build"
