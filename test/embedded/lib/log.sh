#!/usr/bin/env bash
# test/embedded/lib/log.sh
# Shared colour-coded logging helpers for the embedded-containerd test suite.
# Source this file; do not execute directly.

# Colours (disabled when not a tty or when NO_COLOR is set)
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

# log_info  <message>
log_info() { echo -e "${_CYAN}[INFO]${_RESET}  $*"; }

# log_step  <message>  — highlighted step heading
log_step() { echo -e "\n${_BOLD}${_BLUE}==> $*${_RESET}"; }

# log_ok    <message>
log_ok()   { echo -e "${_GREEN}[ OK ]${_RESET}  $*"; }

# log_warn  <message...>
log_warn() { echo -e "${_YELLOW}[WARN]${_RESET}  $*" >&2; }

# log_fail  <message>
log_fail() { echo -e "${_RED}[FAIL]${_RESET}  $*" >&2; }

# log_success <message>
log_success() { echo -e "\n${_BOLD}${_GREEN}✓ $*${_RESET}"; }

# die <message>  — print error and exit 1
die() {
    echo -e "${_RED}[ERROR]${_RESET} $*" >&2
    exit 1
}

# assert_ok <description> <command...>
# Runs a command; prints OK or FAIL and increments global counters.
TESTS_PASSED=0
TESTS_FAILED=0
assert_ok() {
    local desc="$1"; shift
    if "$@" &>/dev/null; then
        log_ok "${desc}"
        (( TESTS_PASSED++ )) || true
    else
        log_fail "${desc}"
        (( TESTS_FAILED++ )) || true
    fi
}

# assert_ok_verbose <description> <command...>
# Like assert_ok, but prints captured output when the command fails.
assert_ok_verbose() {
    local desc="$1"; shift
    local output=""
    local status=0
    output="$("$@" 2>&1)" || status=$?
    if [[ "${status}" -eq 0 ]]; then
        log_ok "${desc}"
        (( TESTS_PASSED++ )) || true
    else
        log_fail "${desc} (command exited ${status}; output: ${output})"
        (( TESTS_FAILED++ )) || true
    fi
}

# assert_contains <description> <substring> <command...>
# Runs a command and checks its output contains the given substring.
# Captures non-zero exit without tripping set -e (command substitution otherwise aborts).
assert_contains() {
    local desc="$1"
    local needle="$2"
    shift 2
    local output
    local status=0
    output="$("$@" 2>&1)" || status=$?
    if [[ "${status}" -eq 0 ]] && echo "${output}" | grep -q "${needle}"; then
        log_ok "${desc}"
        (( TESTS_PASSED++ )) || true
    elif [[ "${status}" -ne 0 ]]; then
        log_fail "${desc} (command exited ${status}; output: ${output})"
        (( TESTS_FAILED++ )) || true
    else
        log_fail "${desc} (expected '${needle}' in output: ${output})"
        (( TESTS_FAILED++ )) || true
    fi
}

# native_arch — returns the real CPU arch even when running under Rosetta 2.
# On Apple Silicon Macs that have Intel bash (from migrated Homebrew), `uname -m`
# returns x86_64 inside the translated process. We detect the truth via sysctl.
native_arch() {
    local arch
    arch="$(uname -m)"
    if [[ "${arch}" == "x86_64" && "$(uname -s)" == "Darwin" ]]; then
        if sysctl -n hw.optional.arm64 2>/dev/null | grep -q 1; then
            echo "arm64"
            return
        fi
    fi
    echo "${arch}"
}

# print_summary — call at the end of a test script
print_summary() {
    local total=$(( TESTS_PASSED + TESTS_FAILED ))
    echo ""
    echo -e "${_BOLD}Results: ${_GREEN}${TESTS_PASSED}${_RESET}/${_BOLD}${total}${_RESET} passed"
    if [[ "${TESTS_FAILED}" -gt 0 ]]; then
        echo -e "${_RED}${_BOLD}${TESTS_FAILED} test(s) FAILED${_RESET}"
        return 1
    else
        log_success "All ${total} tests passed."
        return 0
    fi
}
