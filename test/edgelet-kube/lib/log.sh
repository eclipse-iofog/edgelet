#!/usr/bin/env bash
# Shared logging helpers for the edgelet-kube Lima suite.
# Source this file; do not execute it directly.

if [[ -t 1 && -z "${NO_COLOR:-}" ]]; then
    _RESET='\033[0m'
    _BOLD='\033[1m'
    _CYAN='\033[0;36m'
    _GREEN='\033[0;32m'
    _YELLOW='\033[0;33m'
    _RED='\033[0;31m'
    _BLUE='\033[0;34m'
else
    _RESET='' _BOLD='' _CYAN='' _GREEN='' _YELLOW='' _RED='' _BLUE=''
fi

log_info() { echo -e "${_CYAN}[INFO]${_RESET}  $*"; }
log_step() { echo -e "\n${_BOLD}${_BLUE}==> $*${_RESET}"; }
log_ok()   { echo -e "${_GREEN}[ OK ]${_RESET}  $*"; }
log_warn() { echo -e "${_YELLOW}[WARN]${_RESET}  $*" >&2; }
log_fail() { echo -e "${_RED}[FAIL]${_RESET}  $*" >&2; }
log_success() { echo -e "\n${_BOLD}${_GREEN}✓ $*${_RESET}"; }

die() {
    echo -e "${_RED}[ERROR]${_RESET} $*" >&2
    exit 1
}

# The suite never starts or stops the agent Lima VM.
refuse_agent_vm() {
    if [[ "$1" == "iofog-test" ]]; then
        die "refusing to start or stop iofog-test"
    fi
}

# native_arch returns the CPU arch, including under Rosetta.
native_arch() {
    local arch
    arch="$(uname -m)"
    if [[ "${arch}" == "x86_64" && "$(uname -s)" == "Darwin" ]]; then
        if sysctl -n hw.optional.arm64 2>/dev/null | grep -q 1; then
            echo "arm64"
            return
        fi
    fi
    case "${arch}" in
        aarch64) echo "arm64" ;;
        x86_64)  echo "amd64" ;;
        *)       echo "${arch}" ;;
    esac
}

# go_arch maps a host or guest name onto arm64 or amd64.
go_arch() {
    case "$1" in
        arm64|aarch64) echo "arm64" ;;
        amd64|x86_64)  echo "amd64" ;;
        *) die "unsupported architecture: $1" ;;
    esac
}

# lima_arch maps arm64 or amd64 onto the Lima guest arch name.
lima_arch() {
    case "$(go_arch "$1")" in
        arm64) echo "aarch64" ;;
        amd64) echo "x86_64" ;;
    esac
}

# elf_go_arch reads a Linux ELF and prints arm64 or amd64.
elf_go_arch() {
    local desc
    desc="$(file -b "$1" 2>/dev/null || true)"
    case "${desc}" in
        *aarch64*) echo "arm64" ;;
        *x86-64*|*x86_64*) echo "amd64" ;;
        *) echo "unknown" ;;
    esac
}
