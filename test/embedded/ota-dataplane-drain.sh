#!/usr/bin/env bash
# Data-plane drain checks for a fat embed change and a thin (hash-matched) upgrade.
#
# Host (repo checkout, no VM): docs, no cursor links, KillMode=process.
#   ./test/embedded/ota-dataplane-drain.sh --docs
#
# Live legs inside the embedded Lima VM (after vm-install.sh):
#   ./test/embedded/ota-dataplane-drain.sh --vm-name=iofog-test
#
# Fat embed hash change (second thin binary whose embed hash differs).
# That leg remounts /run noexec for the replace, then restores the previous
# option. The drain runtime is staged under the data directory.
#   ./test/embedded/ota-dataplane-drain.sh --vm-name=iofog-test \
#       --upgrade-bin=build/edgelet-linux-arm64
#
# The live script deploys one private VOLUME workload, restarts control only
# while the ready embed hash matches, then holds an exclusive lock on that
# volume and drains through CRI. Volume files are not deleted.

set -euo pipefail

# Guest runs as a copied file. A stdin launch (bash -s) has no BASH_SOURCE under set -u.
_script="${BASH_SOURCE[0]:-$0}"
if [[ -n "${_script}" && -f "${_script}" ]]; then
    SCRIPT_DIR="$(cd "$(dirname "${_script}")" && pwd)"
    REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
else
    SCRIPT_DIR=""
    REPO_ROOT=""
fi

# Survives run_guest so the EXIT trap can read it after the function returns.
lock_pid=""

DOCS_ONLY=0
GUEST=0
VM_NAME=""
UPGRADE_BIN=""

for arg in "$@"; do
    case "${arg}" in
        --docs) DOCS_ONLY=1 ;;
        --guest) GUEST=1 ;;
        --vm-name=*) VM_NAME="${arg#*=}" ;;
        --upgrade-bin=*) UPGRADE_BIN="${arg#*=}" ;;
        -h|--help)
            if [[ -n "${_script}" && -f "${_script}" ]]; then
                sed -n '2,18p' "${_script}"
            fi
            exit 0
            ;;
        *)
            echo "ERROR: unknown argument: ${arg}" >&2
            exit 2
            ;;
    esac
done

fail() {
    echo "ERROR: $*" >&2
    exit 1
}

check_static() {
    local inst="${REPO_ROOT}/docs/edgelet/installation.md"
    local trouble="${REPO_ROOT}/docs/edgelet/troubleshooting.md"
    local unit_dp="${REPO_ROOT}/packaging/init/systemd/edgelet-containerd.service"
    local unit_ctl="${REPO_ROOT}/packaging/init/systemd/edgelet.service"

    [[ -f "${inst}" ]] || fail "missing ${inst}"
    [[ -f "${trouble}" ]] || fail "missing ${trouble}"
    grep -q 'Fat OTA and thin OTA' "${inst}" \
        || fail "installation.md missing fat vs thin OTA"
    grep -q 'Drain labeled workloads' "${inst}" \
        || fail "installation.md missing data-plane drain order"
    grep -q 'Leftover process holding a volume' "${trouble}" \
        || fail "troubleshooting.md missing leftover process section"
    grep -q 'edgelet volume rm' "${trouble}" \
        || fail "troubleshooting.md missing volume rm warning"
    grep -q 'data/.runtime-drain' "${inst}" \
        || fail "installation.md missing drain staging path"
    grep -q 'noexec' "${inst}" \
        || fail "installation.md missing noexec note"
    grep -q 'Upgrade stays on the old version' "${trouble}" \
        || fail "troubleshooting.md missing stuck-upgrade section"
    grep -q 'permission denied' "${trouble}" \
        || fail "troubleshooting.md missing drain permission denied"
    grep -q 'ota-install.log' "${trouble}" \
        || fail "troubleshooting.md missing OTA install log"
    if grep -R -n --include='*.md' '\.cursor/' "${REPO_ROOT}/docs/edgelet" >/dev/null; then
        fail "operator docs link outside the public doc tree"
    fi
    grep -q 'KillMode=process' "${unit_dp}" || fail "data-plane unit KillMode is not process"
    grep -q 'KillMode=process' "${unit_ctl}" || fail "control unit KillMode is not process"
    if grep -q 'KillMode=control-group' "${unit_dp}" "${unit_ctl}"; then
        fail "KillMode=control-group is set"
    fi
    echo ">>> PASS: docs, no .cursor links, KillMode=process"
}

run_guest() {
    local name="ota-drain-hold"
    local disk="/var/lib/edgelet"
    local shim_file
    shim_file="$(mktemp)"
    lock_pid=""
    run_exec_restore=0

    command -v edgelet >/dev/null || fail "edgelet is not installed"
    command -v python3 >/dev/null || fail "python3 is required for the volume lock fixture"
    [[ "$(id -u)" -eq 0 ]] || fail "live checks must run as root"

    cleanup_lock() {
        local pid="${lock_pid:-}"
        lock_pid=""
        if [[ -n "${pid}" ]] && kill -0 "${pid}" 2>/dev/null; then
            kill -KILL "${pid}" 2>/dev/null || true
            wait "${pid}" 2>/dev/null || true
        fi
    }
    restore_run_exec() {
        if [[ "${run_exec_restore:-0}" -eq 1 ]]; then
            mount -o remount,exec /run || echo "WARNING: could not restore exec on /run" >&2
            run_exec_restore=0
        fi
    }
    cleanup_guest() {
        restore_run_exec
        cleanup_lock
    }
    trap cleanup_guest EXIT

    ms_uuid() {
        edgelet ms inspect "edgelet.${name}" 2>/dev/null \
            | sed -n 's/^  "uuid": "\([^"]*\)".*/\1/p' | head -n1
    }

    wait_running() {
        local i inspect
        for i in $(seq 1 90); do
            inspect="$(edgelet ms inspect "edgelet.${name}" 2>/dev/null || true)"
            if echo "${inspect}" | grep -Eq '^  "state": "running"'; then
                return 0
            fi
            sleep 2
        done
        echo "workload ${name} did not reach running" >&2
        edgelet ms inspect "edgelet.${name}" >&2 || true
        return 1
    }

    holder_gone() {
        local pid="$1" state
        if ! kill -0 "${pid}" 2>/dev/null; then
            return 0
        fi
        state="$(awk '{print $3}' "/proc/${pid}/stat" 2>/dev/null || echo gone)"
        [[ "${state}" == "Z" || "${state}" == "gone" ]]
    }

    embed_hash_of() {
        "$1" version --verbose 2>/dev/null | sed -n 's/^  embed hash: //p' | head -1
    }

    ready_current_hash() {
        local cur fat
        cur="$(readlink -f "${disk}/data/current" 2>/dev/null || true)"
        fat="${cur}/bin/edgelet"
        [[ -n "${cur}" && -x "${fat}" ]] || return 0
        basename "${cur}"
    }

    start_lock() {
        local path="$1"
        python3 - "${path}" <<'PY' &
import fcntl, signal, sys, time
signal.signal(signal.SIGTERM, signal.SIG_IGN)
f = open(sys.argv[1], "a+")
fcntl.flock(f.fileno(), fcntl.LOCK_EX)
while True:
    time.sleep(3600)
PY
        lock_pid=$!
        # Drop the job so a later SIGKILL is not reported as a shell job death.
        disown "${lock_pid}" 2>/dev/null || true
        local i
        for i in $(seq 1 50); do
            if python3 - "${path}" <<'PY'
import fcntl, sys
f = open(sys.argv[1], "a+")
try:
    fcntl.flock(f.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
except BlockingIOError:
    sys.exit(0)
sys.exit(1)
PY
            then
                return 0
            fi
            sleep 0.1
        done
        fail "volume lock fixture did not take the lock"
    }

    lock_is_free() {
        python3 - "$1" <<'PY'
import fcntl, sys
f = open(sys.argv[1], "a+")
try:
    fcntl.flock(f.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
except BlockingIOError:
    sys.exit(1)
fcntl.flock(f.fileno(), fcntl.LOCK_UN)
sys.exit(0)
PY
    }

    assert_no_lock_loop() {
        local uuid="$1"
        if edgelet ms inspect "${uuid}" 2>/dev/null | grep -q 'Cannot lock file'; then
            fail "microservice inspect reports Cannot lock file"
        fi
        if command -v journalctl >/dev/null 2>&1; then
            if journalctl -u edgelet -u edgelet-containerd --since "10 min ago" --no-pager 2>/dev/null \
                | grep -q 'Cannot lock file'; then
                fail "journal reports Cannot lock file"
            fi
        fi
    }

    restart_control_only() {
        if command -v systemctl >/dev/null 2>&1 && systemctl cat edgelet.service >/dev/null 2>&1; then
            systemctl restart edgelet
            return
        fi
        if command -v rc-service >/dev/null 2>&1; then
            rc-service edgelet restart
            return
        fi
        fail "no edgelet control service to restart"
    }

    echo ">>> deploy private volume workload"
    local uuid vol marker lockfile
    uuid="$(ms_uuid || true)"
    if [[ -z "${uuid}" ]]; then
        cat >/tmp/ota-drain-hold.yaml <<'EOF'
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: ota-drain-hold
spec:
  image: docker.io/library/alpine:3.19
  registry: 1
  container:
    hostNetworkMode: false
    isPrivileged: false
    commands:
      - /bin/sh
      - -lc
      - sleep 14000
    volumes:
      - hostDestination: ota-drain-data
        containerDestination: /data
        accessMode: rw
        type: volume
  schedule: 50
EOF
        edgelet deploy -f /tmp/ota-drain-hold.yaml >/dev/null
    fi
    wait_running || fail "workload did not reach running"
    uuid="$(ms_uuid || true)"
    [[ -n "${uuid}" ]] || fail "workload uuid is empty"

    vol="${disk}/volumes/data/${uuid}/ota-drain-data"
    [[ -d "${vol}" ]] || fail "private volume directory missing: ${vol}"
    marker="${vol}/marker"
    lockfile="${vol}/status"
    echo keep-ota-drain > "${marker}"
    : > "${lockfile}"

    echo ">>> hash unchanged: control restart leaves shim processes"
    local current newbin
    current="$(ready_current_hash)"
    newbin="$(embed_hash_of /usr/local/bin/edgelet)"
    [[ -n "${current}" && "${current}" == "${newbin}" ]] \
        || fail "ready current ${current} does not match installed embed hash ${newbin}"
    pgrep -f '[c]ontainerd-shim' > "${shim_file}" || fail "no shim processes while the workload is running"
    local shim
    restart_control_only
    sleep 3
    while read -r shim; do
        [[ -n "${shim}" ]] || continue
        kill -0 "${shim}" 2>/dev/null || fail "shim ${shim} exited after control restart"
    done < "${shim_file}"
    wait_running || fail "workload not running after control restart"
    [[ -f "${marker}" ]] || fail "volume marker disappeared during control restart"

    echo ">>> exclusive lock on the private volume, then CRI drain"
    start_lock "${lockfile}"
    local holder="${lock_pid}"
    if ! edgelet --quiet runtime drain --direct --timeout 90; then
        fail "data-plane drain did not verify"
    fi
    if ! holder_gone "${holder}"; then
        fail "lock holder ${holder} still running after drain"
    fi
    wait "${holder}" 2>/dev/null || true
    lock_pid=""
    lock_is_free "${lockfile}" || fail "exclusive lock still held after drain"
    [[ -d "${vol}" && -f "${marker}" ]] || fail "volume data was removed during drain"
    wait_running || fail "workload did not return after drain"
    assert_no_lock_loop "${uuid}"

    if [[ -x /tmp/ota-drain-upgrade-bin && -f /tmp/ota-drain-install.sh ]]; then
        local upgrade_hash
        upgrade_hash="$(embed_hash_of /tmp/ota-drain-upgrade-bin)"
        if [[ -z "${upgrade_hash}" || "${upgrade_hash}" == "${current}" ]]; then
            echo ">>> upgrade binary embed hash matches current; skipping fat replace"
        else
            echo ">>> fat embed hash change ${current} -> ${upgrade_hash} with /run noexec"
            local receipt="/var/backups/edgelet/install-receipt"
            local before_sha=""
            if [[ -f "${receipt}" ]]; then
                before_sha="$(sed -n 's/^binary_sha256=//p' "${receipt}" | head -n1)"
            fi
            if findmnt -n -o OPTIONS /run | tr ',' '\n' | grep -qx noexec; then
                echo ">>> /run is already noexec"
            else
                mount -o remount,noexec /run || fail "could not remount /run noexec"
                run_exec_restore=1
            fi
            findmnt -n -o OPTIONS /run | tr ',' '\n' | grep -qx noexec \
                || fail "/run is not mounted noexec"
            start_lock "${lockfile}"
            holder="${lock_pid}"
            bash /tmp/ota-drain-install.sh --upgrade --airgap --bin-path=/tmp/ota-drain-upgrade-bin
            local staged="${disk}/data/.runtime-drain/edgelet"
            [[ -f "${staged}" ]] || fail "drain runtime was not staged at ${staged}"
            if ! holder_gone "${holder}"; then
                fail "lock holder ${holder} survived fat replace"
            fi
            wait "${holder}" 2>/dev/null || true
            lock_pid=""
            lock_is_free "${lockfile}" || fail "exclusive lock still held after fat replace"
            [[ -d "${vol}" && -f "${marker}" ]] || fail "volume data was removed during fat replace"
            local after
            after="$(ready_current_hash)"
            [[ "${after}" == "${upgrade_hash}" ]] || fail "ready current ${after} != upgrade hash ${upgrade_hash}"
            local after_sha=""
            [[ -f "${receipt}" ]] || fail "install receipt missing after fat upgrade"
            after_sha="$(sed -n 's/^binary_sha256=//p' "${receipt}" | head -n1)"
            [[ -n "${after_sha}" && "${after_sha}" != "${before_sha}" ]] \
                || fail "install receipt binary_sha256 did not change"
            uuid="$(ms_uuid || true)"
            [[ -n "${uuid}" ]] || fail "workload missing after fat replace"
            vol="${disk}/volumes/data/${uuid}/ota-drain-data"
            wait_running || fail "workload not running after fat replace"
            assert_no_lock_loop "${uuid}"
        fi
    else
        echo ">>> fat embed replace not requested (pass --upgrade-bin to run it)"
    fi

    echo ">>> PASS: control restart kept shims; drain released the volume lock"
    trap - EXIT
    cleanup_guest
}

if [[ "${GUEST}" -eq 1 ]]; then
    run_guest
    exit 0
fi

check_static

if [[ "${DOCS_ONLY}" -eq 1 && -z "${VM_NAME}" ]]; then
    exit 0
fi

if [[ -z "${VM_NAME}" ]]; then
    echo "Live drain checks were not run. On a host with the embedded Lima VM:"
    echo "  ./test/embedded/ota-dataplane-drain.sh --vm-name=iofog-test"
    echo "Fat embed hash change also needs a second binary:"
    echo "  ./test/embedded/ota-dataplane-drain.sh --vm-name=iofog-test --upgrade-bin=build/edgelet-linux-arm64"
    exit 0
fi

command -v limactl >/dev/null || fail "limactl is required for --vm-name"
if ! limactl list 2>/dev/null | grep -q "${VM_NAME}"; then
    fail "Lima VM ${VM_NAME} is not running. Start it with ./test/embedded/vm-start.sh then re-run this script."
fi

echo ">>> staging install.sh into ${VM_NAME}"
limactl --tty=false shell "${VM_NAME}" -- sudo tee /tmp/ota-drain-install.sh >/dev/null < "${REPO_ROOT}/install.sh"
if [[ -n "${UPGRADE_BIN}" ]]; then
    [[ -f "${UPGRADE_BIN}" ]] || fail "upgrade binary not found: ${UPGRADE_BIN}"
    limactl copy "${UPGRADE_BIN}" "${VM_NAME}:/tmp/ota-drain-upgrade-bin"
    limactl --tty=false shell "${VM_NAME}" -- sudo chmod 755 /tmp/ota-drain-upgrade-bin
fi

limactl --tty=false shell "${VM_NAME}" -- sudo tee /tmp/ota-dataplane-drain.sh >/dev/null < "${_script}"
limactl --tty=false shell "${VM_NAME}" -- sudo bash /tmp/ota-dataplane-drain.sh --guest
