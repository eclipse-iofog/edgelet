#!/usr/bin/env bash
# Ready-current embed hash gate and fat upgrade order (no live fleet).
#
# Usage:
#   ./test/install/install-embed-hash-gate.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
LIB="${REPO_ROOT}/scripts/lib/init-edgelet.sh"
GEN="${REPO_ROOT}/scripts/install/gen-embedded-block.sh"
INSTALL_SH="${REPO_ROOT}/install.sh"

fail() {
    echo "ERROR: $*" >&2
    exit 1
}

[[ -f "${LIB}" ]] || fail "missing ${LIB}"
[[ -f "${GEN}" ]] || fail "missing ${GEN}"
[[ -f "${INSTALL_SH}" ]] || fail "missing ${INSTALL_SH}"

die() { echo "ERROR: $1" >&2; exit 1; }
info() { echo ">>> $1"; }

# shellcheck source=scripts/lib/init-edgelet.sh
source "${LIB}"

make_ready_bundle() {
    local root="$1" hash="$2"
    local dir="${root}/data/${hash}/bin"
    mkdir -p "${dir}/aux"
    printf '\177ELF\002\001' > "${dir}/edgelet"
    chmod 755 "${dir}/edgelet"
    printf 'shim' > "${dir}/containerd-shim-runc-v2"
    printf 'x' > "${dir}/aux/xtables-legacy-multi"
    printf 'ip' > "${dir}/ip"
    printf 'bb' > "${dir}/busybox"
    ln -sfn xtables-legacy-multi "${dir}/aux/iptables"
}

link_current() {
    local root="$1" target="$2"
    ln -sfn "${target}" "${root}/data/current"
}

restart_required() {
    if should_restart_data_plane "$1" "$2" "$3"; then
        echo true
    else
        echo false
    fi
}

echo ">>> ready current matches new embed hash"
ROOT=$(mktemp -d)
trap 'rm -rf "${ROOT}"' EXIT
export EDGELET_DATA_DIR="${ROOT}"
HASH_A="aaaa1111aaaa1111"
HASH_B="bbbb2222bbbb2222"
make_ready_bundle "${ROOT}" "${HASH_A}"
link_current "${ROOT}" "${ROOT}/data/${HASH_A}"
GOT=$(installed_embed_hash)
[[ "${GOT}" == "${HASH_A}" ]] || fail "ready current hash ${GOT} != ${HASH_A}"
[[ "$(restart_required edgelet "${GOT}" "${HASH_A}")" == "false" ]] || fail "matching ready hash must not restart the data plane"

echo ">>> ready current differs from new embed hash"
[[ "$(restart_required edgelet "${GOT}" "${HASH_B}")" == "true" ]] || fail "different ready hash must restart the data plane"

echo ">>> current symlink missing"
rm -f "${ROOT}/data/current"
GOT=$(installed_embed_hash)
[[ -z "${GOT}" ]] || fail "missing current returned ${GOT}"
[[ "$(restart_required edgelet "${GOT}" "${HASH_B}")" == "true" ]] || fail "missing current must restart the data plane when a new hash is set"
[[ "$(restart_required edgelet "" "")" == "false" ]] || fail "empty new hash must not restart the data plane"

echo ">>> incomplete bundle is not installed"
mkdir -p "${ROOT}/data/${HASH_B}"
link_current "${ROOT}" "${ROOT}/data/${HASH_B}"
GOT=$(installed_embed_hash)
[[ -z "${GOT}" ]] || fail "incomplete current counted as installed (${GOT})"
[[ "$(restart_required edgelet "${GOT}" "${HASH_B}")" == "true" ]] || fail "incomplete current must restart the data plane"

echo ">>> non-ELF fat runtime is not ready"
make_ready_bundle "${ROOT}" "${HASH_A}"
printf 'not-elf' > "${ROOT}/data/${HASH_A}/bin/edgelet"
chmod 755 "${ROOT}/data/${HASH_A}/bin/edgelet"
link_current "${ROOT}" "${HASH_A}"
GOT=$(installed_embed_hash)
[[ -z "${GOT}" ]] || fail "non-ELF current counted as installed (${GOT})"

echo ">>> docker and podman never restart the data plane"
[[ "$(restart_required docker "${HASH_A}" "${HASH_B}")" == "false" ]] || fail "docker must not restart edgelet-containerd"
[[ "$(restart_required podman "" "${HASH_B}")" == "false" ]] || fail "podman must not restart edgelet-containerd"

echo ">>> authoring hash helpers match the install monolith generator"
extract_helpers() {
    awk '
        /^edgelet_data_root\(\)/ { keep=1 }
        keep && /^install_systemd_dropin\(\)/ { exit }
        keep { print }
    ' "$1"
}
diff -u <(extract_helpers "${LIB}") <(extract_helpers "${GEN}") \
    || fail "init helpers drifted from gen-embedded-block.sh"

grep -q 'quiesce_data_plane_for_replace' "${INSTALL_SH}" \
    || fail "install.sh missing data-plane quiesce — run make install-scripts"
grep -q 'embed_bundle_ready' "${INSTALL_SH}" \
    || fail "install.sh missing ready-current check — run make install-scripts"

extract_fn_bodies() {
    awk '
        /^replace_staged_edgelet_binary\(\)/ { found=1 }
        found { print }
        found && $0 == "}" { found=0; print "---END---" }
    ' "${INSTALL_SH}"
}

echo ">>> fat replace drains before stopping the data plane and before installing the binary"
BODY=$(extract_fn_bodies)
[[ -n "${BODY}" ]] || fail "replace_staged_edgelet_binary not found in install.sh"
printf '%s\n' "${BODY}" | awk '
    function reset() { ln = q = stop_dp = stop_ctl = inst = 0; line_dp = ""; line_ctl = "" }
    $0 == "---END---" {
        n++
        if (q == 0 || stop_dp == 0 || stop_ctl == 0 || inst == 0) {
            print "replace function missing drain, stop, or install" > "/dev/stderr"
            exit 1
        }
        if (!(q < stop_dp && stop_dp < inst && q < stop_ctl)) {
            print "drain must run before data-plane stop, control stop, and binary install" > "/dev/stderr"
            exit 1
        }
        if (line_dp == line_ctl) {
            print "data-plane stop and control stop must be different branches" > "/dev/stderr"
            exit 1
        }
        reset()
        next
    }
    {
        if (ln == 0) reset()
        ln++
        if (q == 0 && index($0, "quiesce_data_plane_for_replace")) q = ln
        if (stop_dp == 0 && index($0, "stop_edgelet_containerd_unit")) { stop_dp = ln; line_dp = $0 }
        if (stop_ctl == 0 && index($0, "stop_edgelet_service")) { stop_ctl = ln; line_ctl = $0 }
        if (inst == 0 && index($0, "install_binary_file")) inst = ln
    }
    END {
        if (n < 1) {
            print "no function body" > "/dev/stderr"
            exit 1
        }
        printf "checked %d replace function body(ies)\n", n
    }
' || fail "fat replace order"

section_order() {
    local marker="$1"
    awk -v marker="${marker}" '
        function reset_section() { rep = init = receipt = stopped = 0; ln = 0 }
        function close_section() {
            n++
            if (rep == 0 || init == 0 || receipt == 0) {
                printf "%s missing replace, init, or receipt\n", marker > "/dev/stderr"
                exit 1
            }
            if (!(rep < init && init < receipt)) {
                printf "%s must replace, then start services, then write the receipt\n", marker > "/dev/stderr"
                exit 1
            }
            if (stopped) {
                printf "%s must not stop control before the replace helper\n", marker > "/dev/stderr"
                exit 1
            }
        }
        index($0, marker) == 1 {
            if (insec) close_section()
            insec = 1
            reset_section()
            next
        }
        insec && index($0, "# ── ") == 1 {
            close_section()
            insec = 0
            next
        }
        insec {
            ln++
            if (rep == 0 && index($0, "replace_staged_edgelet_binary")) rep = ln
            if (init == 0 && index($0, "install_init_unit")) init = ln
            if (receipt == 0 && index($0, "write_install_receipt")) receipt = ln
            if (index($0, "stop_edgelet_service")) stopped = 1
        }
        END {
            if (insec) close_section()
            if (n < 1) {
                printf "missing %s\n", marker > "/dev/stderr"
                exit 1
            }
            printf "%s order ok (%d block(s))\n", marker, n
        }
    ' "${INSTALL_SH}" || fail "section order: ${marker}"
}

echo ">>> upgrade writes the receipt only after replace and service start"
section_order "# ── upgrade "

echo ">>> rollback uses the same replace order as upgrade"
section_order "# ── rollback "

echo ">>> every init family starts the data plane before restarting control"
awk '
    BEGIN {
        ctrl["systemd"] = "systemctl stop edgelet"
        ctrl["openrc"] = "rc-service edgelet restart"
        ctrl["procd"] = "/etc/init.d/edgelet stop"
        ctrl["sysvinit"] = "/etc/init.d/edgelet restart"
        ctrl["upstart"] = "initctl restart edgelet"
        ctrl["s6"] = "s6-svc -d"
        ctrl["runit"] = "sv restart edgelet"
    }
    function validate() {
        if (!(name in ctrl)) return
        if (dp < 1 || ctl < 1 || dp > ctl) {
            printf "%s: data plane must start before control restart\n", name > "/dev/stderr"
            exit 1
        }
        seen++
    }
    /^install_init_unit\(\)/ { infunc = 1; next }
    infunc && /^stop_edgelet_service\(\)/ {
        if (name != "") validate()
        if (seen != 7) {
            printf "expected 7 init families, saw %d\n", seen > "/dev/stderr"
            exit 1
        }
        print "init order ok"
        exit 0
    }
    infunc && /^        [a-z0-9*]+\)$/ {
        if (name != "") validate()
        name = $0
        sub(/^        /, "", name)
        sub(/\)$/, "", name)
        dp = ctl = 0
        next
    }
    infunc && name != "" {
        if (dp == 0 && index($0, "start_edgelet_containerd_unit")) dp = NR
        if (name in ctrl && ctl == 0 && index($0, ctrl[name])) ctl = NR
    }
' "${LIB}" || fail "init family order"

echo ">>> verify failure does not replace the binary"
WORK=$(mktemp -d)
trap 'rm -rf "${ROOT}" "${WORK}"' EXIT
export EDGELET_DATA_DIR="${WORK}"
make_ready_bundle "${WORK}" "${HASH_A}"
link_current "${WORK}" "${WORK}/data/${HASH_A}"
BIN_DIR="${WORK}/bin"
mkdir -p "${BIN_DIR}"
cat > "${BIN_DIR}/edgelet" << 'EOF'
#!/bin/sh
if [ "$1" = "version" ]; then
    printf '%s\n' "  embed hash: bbbb2222bbbb2222"
    exit 0
fi
exit 1
EOF
chmod 755 "${BIN_DIR}/edgelet"
INSTALLED="${WORK}/installed-edgelet"
printf 'original-binary\n' > "${INSTALLED}"
export OS=linux
export INIT=testinit
export CONTAINER_ENGINE=edgelet
export BINARY_PATH="${INSTALLED}"
REPLACED="${WORK}/replaced"
stop_edgelet_containerd_unit() { echo stopped-dp > "${WORK}/stopped-dp"; }
stop_edgelet_service() { echo stopped-control > "${WORK}/stopped-control"; }
install_binary_file() { echo replaced > "${REPLACED}"; cp "$1" "$2"; }

eval "$(awk '
    /^replace_staged_edgelet_binary\(\)/ { found=1 }
    found { print }
    found && $0 == "}" { exit }
' "${INSTALL_SH}")"

set +e
(
    set -e
    replace_staged_edgelet_binary "${BIN_DIR}/edgelet"
)
RC=$?
set -e
[[ "${RC}" -ne 0 ]] || fail "verify failure must be non-zero"
[[ ! -f "${REPLACED}" ]] || fail "verify failure replaced the binary"
grep -q 'original-binary' "${INSTALLED}" || fail "installed binary changed after verify failure"
[[ ! -f "${WORK}/stopped-dp" ]] || fail "verify failure stopped the data plane"

echo ">>> matching hash does not stop containerd; docker does not either"
rm -f "${REPLACED}" "${WORK}/stopped-dp" "${WORK}/stopped-control"
cat > "${BIN_DIR}/edgelet" << EOF
#!/bin/sh
if [ "\$1" = "version" ]; then
    printf '%s\n' "  embed hash: ${HASH_A}"
    exit 0
fi
exit 1
EOF
chmod 755 "${BIN_DIR}/edgelet"
replace_staged_edgelet_binary "${BIN_DIR}/edgelet"
[[ ! -f "${WORK}/stopped-dp" ]] || fail "matching embed hash stopped the data plane"
[[ -f "${WORK}/stopped-control" ]] || fail "matching embed hash should restart control"
[[ -f "${REPLACED}" ]] || fail "matching embed hash should still install the thin binary"

rm -f "${REPLACED}" "${WORK}/stopped-dp" "${WORK}/stopped-control"
export CONTAINER_ENGINE=docker
cat > "${BIN_DIR}/edgelet" << 'EOF'
#!/bin/sh
if [ "$1" = "version" ]; then
    printf '%s\n' "  embed hash: bbbb2222bbbb2222"
    exit 0
fi
exit 0
EOF
chmod 755 "${BIN_DIR}/edgelet"
replace_staged_edgelet_binary "${BIN_DIR}/edgelet"
[[ ! -f "${WORK}/stopped-dp" ]] || fail "docker engine stopped edgelet-containerd"
[[ -f "${REPLACED}" ]] || fail "docker engine should still install the thin binary"

export CONTAINER_ENGINE=podman
rm -f "${REPLACED}" "${WORK}/stopped-dp"
replace_staged_edgelet_binary "${BIN_DIR}/edgelet"
[[ ! -f "${WORK}/stopped-dp" ]] || fail "podman engine stopped edgelet-containerd"

echo ">>> PASS: ready-current hash gate and fat replace order"
