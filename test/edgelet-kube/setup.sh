#!/usr/bin/env bash
# Checks the host tools for the edgelet-kube Lima suite.
# Darwin builds the Linux binaries inside Docker, so a cross compiler is not required.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/log.sh
source "${SCRIPT_DIR}/lib/log.sh"

if [[ "$(uname -s)" == "Darwin" ]]; then
    if ! command -v brew >/dev/null 2>&1; then
        die "Homebrew is not installed. Install it from https://brew.sh and re-run."
    fi
    log_info "Homebrew: OK ($(brew --version | head -1))"
fi

if ! command -v limactl >/dev/null 2>&1; then
    if [[ "$(uname -s)" == "Darwin" ]]; then
        log_info "Installing Lima..."
        brew install lima
    else
        die "limactl not found"
    fi
fi
log_info "Lima: OK ($(limactl --version 2>/dev/null | head -1))"

if ! command -v jq >/dev/null 2>&1; then
    if [[ "$(uname -s)" == "Darwin" ]]; then
        log_info "Installing jq..."
        brew install jq
    else
        die "jq not found"
    fi
fi
log_info "jq: OK ($(jq --version))"

if ! command -v file >/dev/null 2>&1; then
    die "file(1) is required to check the Linux binaries"
fi

if [[ "$(uname -s)" == "Darwin" ]]; then
    if ! command -v docker >/dev/null 2>&1; then
        die "Docker is required to build the Linux binaries on macOS"
    fi
    if ! docker info >/dev/null 2>&1; then
        die "Docker is installed but the daemon is not running"
    fi
    log_info "Docker: OK ($(docker version --format '{{.Server.Version}}' 2>/dev/null || echo running))"
fi

log_success "Prerequisites are present. Run ./test/edgelet-kube/run-all.sh"
