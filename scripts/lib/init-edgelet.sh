# init-edgelet.sh — install linux init unit for edgelet (sourced by install.sh)

EDGELET_LIBEXEC="/usr/libexec/edgelet"
EDGELET_INIT_SHARE="/usr/share/edgelet/init"

init_packaging_root() {
    if [ -n "${EDGELET_INIT_DIR}" ] && [ -d "${EDGELET_INIT_DIR}" ]; then
        echo "${EDGELET_INIT_DIR}"
        return 0
    fi
    if [ -d "${SCRIPT_DIR}/packaging/init" ]; then
        echo "${SCRIPT_DIR}/packaging/init"
        return 0
    fi
    if [ -d "${EDGELET_INIT_SHARE}" ]; then
        echo "${EDGELET_INIT_SHARE}"
        return 0
    fi
    die "Init templates not found (packaging/init or ${EDGELET_INIT_SHARE})"
}

edgelet_shutdown_script() {
    if [ -f "${SCRIPT_DIR}/scripts/edgelet-shutdown" ]; then
        echo "${SCRIPT_DIR}/scripts/edgelet-shutdown"
        return 0
    fi
    if [ -f "${SHARE_DIR}/edgelet-shutdown" ]; then
        echo "${SHARE_DIR}/edgelet-shutdown"
        return 0
    fi
    die "Missing edgelet-shutdown helper (scripts/edgelet-shutdown)"
}

install_init_helpers() {
    _shutdown="$(edgelet_shutdown_script)"
    mkdir -p "${EDGELET_LIBEXEC}"
    install -m 755 "${_shutdown}" "${EDGELET_LIBEXEC}/edgelet-shutdown"
}

openrc_engine_need_line() {
    case "$1" in
        docker) printf '%s\n' '    need docker' ;;
        podman) printf '%s\n' '    need podman' ;;
        edgelet) printf '%s\n' '    need edgelet-containerd' ;;
        *)      printf '%s\n' '' ;;
    esac
}

apply_openrc_engine_deps() {
    _eng="$1"
    _dest="$2"
    _need="$(openrc_engine_need_line "${_eng}")"
    if [ -f "${_dest}" ]; then
        # shellcheck disable=SC2016
        awk -v need="${_need}" '
            /%%EDGELET_ENGINE_NEED%%/ {
                if (need != "") print need
                next
            }
            { print }
        ' "${_dest}" > "${_dest}.tmp" && mv "${_dest}.tmp" "${_dest}"
    fi
}

edgelet_data_root() {
    if [ -n "${EDGELET_DATA_DIR:-}" ]; then
        echo "${EDGELET_DATA_DIR}"
        return 0
    fi
    echo "/var/lib/edgelet"
}

# embed_bundle_ready is true when dir has an executable fat runtime and the
# same companion files the extract path requires before it will run.
embed_bundle_ready() {
    _dir="$1"
    [ -n "$_dir" ] || return 1
    _fat="${_dir}/bin/edgelet"
    [ -f "$_fat" ] && [ -x "$_fat" ] || return 1
    _magic=$(od -An -tx1 -N 4 "$_fat" 2>/dev/null | tr -d ' \n' || true)
    [ "$_magic" = "7f454c46" ] || return 1
    [ -f "${_dir}/bin/containerd-shim-runc-v2" ] || return 1
    [ -f "${_dir}/bin/aux/xtables-legacy-multi" ] || return 1
    [ -f "${_dir}/bin/ip" ] || return 1
    [ -f "${_dir}/bin/busybox" ] || return 1
    [ -L "${_dir}/bin/aux/iptables" ] || return 1
    [ "$(readlink "${_dir}/bin/aux/iptables" 2>/dev/null || true)" = "xtables-legacy-multi" ]
}

# installed_embed_hash prints the ready data/current bundle hash, or nothing
# when the symlink is missing or the fat runtime is not ready.
installed_embed_hash() {
    _link="$(edgelet_data_root)/data/current"
    [ -L "$_link" ] || return 0
    _target=$(readlink "$_link" 2>/dev/null || true)
    [ -n "$_target" ] || return 0
    case "$_target" in
        /*) ;;
        *) _target="$(CDPATH= cd -- "$(dirname "$_link")" && pwd)/${_target}" ;;
    esac
    embed_bundle_ready "$_target" || return 0
    basename "$_target"
}

# binary_embed_hash reads embed hash from a linux thin binary (edgelet version --verbose).
binary_embed_hash() {
    _bin="$1"
    [ -x "$_bin" ] || return 0
    "$_bin" version --verbose 2>/dev/null \
        | sed -n 's/^  embed hash: //p' \
        | head -1
}

# should_restart_data_plane is true when containerEngine=edgelet, the new embed
# hash is set, and the ready current bundle is missing or different.
# Returns false for docker/podman or a thin binary with no embed hash.
should_restart_data_plane() {
    _eng="$1"
    _old="$2"
    _new="$3"
    [ "$_eng" = "edgelet" ] || return 1
    [ -n "$_new" ] || return 1
    [ -z "$_old" ] && return 0
    [ "$_old" != "$_new" ]
}

# quiesce_data_plane_for_replace drains labeled workloads, force-stops leftovers,
# and verifies before an embed replace. Control stays up. Non-zero leaves the
# installed binary in place.
quiesce_data_plane_for_replace() {
    _bin="$1"
    if [ -z "$_bin" ] || [ ! -x "$_bin" ]; then
        _bin="${EDGELET_BIN:-/usr/local/bin/edgelet}"
    fi
    [ -x "$_bin" ] || return 1
    info "Draining data plane before embed replace"
    "$_bin" --quiet runtime drain --direct
}

stop_edgelet_containerd_unit() {
    _init="$1"
    info "Stopping edgelet-containerd (data plane)"
    case "${_init}" in
        systemd)
            systemctl stop edgelet-containerd 2>/dev/null || true
            systemctl reset-failed edgelet-containerd 2>/dev/null || true
            ;;
        openrc)
            rc-service edgelet-containerd stop 2>/dev/null || true
            ;;
        *)
            stop_edgelet_dataplane_processes
            ;;
    esac
}

# stop_edgelet_dataplane_processes stops runtime-bootstrap and its containerd
# child. It does not stop the control daemon.
stop_edgelet_dataplane_processes() {
    _pids=$(pgrep -f '[e]dgelet runtime-bootstrap' 2>/dev/null || true)
    _pids="${_pids} $(pgrep -f '[e]dgelet-containerd-child' 2>/dev/null || true)"
    _pids=$(echo "${_pids}" | tr ' ' '\n' | awk 'NF && !seen[$0]++')
    [ -n "${_pids}" ] || return 0
    for _p in ${_pids}; do
        kill -TERM "${_p}" 2>/dev/null || true
    done
    sleep 1
    for _p in ${_pids}; do
        kill -0 "${_p}" 2>/dev/null || continue
        kill -KILL "${_p}" 2>/dev/null || true
    done
}

start_edgelet_dataplane_process() {
    if pgrep -f '[e]dgelet runtime-bootstrap' >/dev/null 2>&1; then
        return 0
    fi
    if pgrep -f '[e]dgelet-containerd-child' >/dev/null 2>&1; then
        return 0
    fi
    mkdir -p /var/log/edgelet
    /usr/local/bin/edgelet runtime-bootstrap >>/var/log/edgelet/containerd.log 2>&1 &
}

start_edgelet_containerd_unit() {
    _init="$1"
    _restart="$2"
    case "${_init}" in
        systemd)
            systemctl enable edgelet-containerd 2>/dev/null || true
            if [ "$_restart" = true ]; then
                info "Embedded bundle hash changed; restarting edgelet-containerd (data plane)"
                stop_edgelet_containerd_unit "${_init}"
                systemctl start edgelet-containerd
            else
                systemctl start edgelet-containerd 2>/dev/null || true
            fi
            ;;
        openrc)
            rc-update add edgelet-containerd default 2>/dev/null || true
            if [ "$_restart" = true ]; then
                info "Embedded bundle hash changed; restarting edgelet-containerd (data plane)"
                stop_edgelet_containerd_unit "${_init}"
                rc-service edgelet-containerd start 2>/dev/null || true
            else
                rc-service edgelet-containerd start 2>/dev/null || true
            fi
            ;;
        *)
            if [ "$_restart" = true ]; then
                info "Embedded bundle hash changed; restarting edgelet-containerd (data plane)"
                stop_edgelet_containerd_unit "${_init}"
                start_edgelet_dataplane_process
            fi
            ;;
    esac
}

install_systemd_dropin() {
    _eng="$1"
    _root="$2"
    _dropdir="/etc/systemd/system/edgelet.service.d"
    mkdir -p "${_dropdir}"
    rm -f "${_dropdir}/docker.conf" "${_dropdir}/podman.conf" "${_dropdir}/edgelet.conf"
    case "${_eng}" in
        docker)
            install -m 644 "${_root}/systemd/edgelet.service.d/docker.conf" "${_dropdir}/docker.conf"
            ;;
        podman)
            install -m 644 "${_root}/systemd/edgelet.service.d/podman.conf" "${_dropdir}/podman.conf"
            ;;
        edgelet)
            install -m 644 "${_root}/systemd/edgelet.service.d/edgelet.conf" "${_dropdir}/edgelet.conf"
            systemctl enable edgelet-containerd 2>/dev/null || true
            ;;
    esac
}

install_init_unit() {
    _init="$1"
    _eng="$2"
    _restart_dp="${3:-false}"
    _root="$(init_packaging_root)"
    mkdir -p /var/log/edgelet
    install_init_helpers

    case "${_init}" in
        systemd)
            _unit="${_root}/systemd/edgelet.service"
            [ -f "$_unit" ] || die "Missing ${_unit}"
            mkdir -p /etc/cni/net.d /run/edgelet /run/containerd
            chmod 755 /run/edgelet /run/containerd 2>/dev/null || true
            install -m 644 "$_unit" /etc/systemd/system/edgelet.service
            _containerd="${_root}/systemd/edgelet-containerd.service"
            if [ -f "${_containerd}" ]; then
                install -m 644 "${_containerd}" /etc/systemd/system/edgelet-containerd.service
            fi
            install_systemd_dropin "${_eng}" "${_root}"
            systemctl daemon-reload
            if [ "${_eng}" = "edgelet" ]; then
                start_edgelet_containerd_unit "${_init}" "${_restart_dp}"
            fi
            systemctl enable edgelet
            systemctl stop edgelet 2>/dev/null || true
            systemctl reset-failed edgelet 2>/dev/null || true
            systemctl start edgelet
            info "systemd unit edgelet.service installed (engine=${_eng} drop-in)."
            ;;
        openrc)
            install -m 755 "${_root}/openrc/edgelet.init" /etc/init.d/edgelet
            apply_openrc_engine_deps "${_eng}" /etc/init.d/edgelet
            chmod 755 /etc/init.d/edgelet
            if [ -f "${_root}/openrc/edgelet-cgroup-prep.init" ]; then
                install -m 755 "${_root}/openrc/edgelet-cgroup-prep.init" /etc/init.d/edgelet-cgroup-prep
                rc-update add edgelet-cgroup-prep sysinit 2>/dev/null || true
            fi
            if [ -f "${_root}/openrc/edgelet-containerd.init" ]; then
                install -m 755 "${_root}/openrc/edgelet-containerd.init" /etc/init.d/edgelet-containerd
            fi
            if [ "${_eng}" = "edgelet" ]; then
                start_edgelet_containerd_unit "${_init}" "${_restart_dp}"
            fi
            rc-update add edgelet default 2>/dev/null || true
            rc-service edgelet restart 2>/dev/null || rc-service edgelet start
            info "OpenRC service edgelet installed (engine=${_eng})."
            ;;
        procd)
            install -m 755 "${_root}/procd/edgelet" /etc/init.d/edgelet
            /etc/init.d/edgelet enable 2>/dev/null || true
            if [ "${_eng}" = "edgelet" ]; then
                start_edgelet_containerd_unit "${_init}" "${_restart_dp}"
            fi
            /etc/init.d/edgelet stop 2>/dev/null || true
            /etc/init.d/edgelet start
            info "procd init script edgelet installed (engine=${_eng})."
            ;;
        sysvinit)
            install -m 755 "${_root}/sysvinit/edgelet.init" /etc/init.d/edgelet
            if command -v update-rc.d >/dev/null 2>&1; then
                update-rc.d edgelet defaults 2>/dev/null || true
            elif command -v chkconfig >/dev/null 2>&1; then
                chkconfig --add edgelet 2>/dev/null || true
            fi
            if [ "${_eng}" = "edgelet" ]; then
                start_edgelet_containerd_unit "${_init}" "${_restart_dp}"
            fi
            /etc/init.d/edgelet restart 2>/dev/null || /etc/init.d/edgelet start
            info "SysV init script edgelet installed."
            ;;
        upstart)
            install -m 644 "${_root}/upstart/edgelet.conf" /etc/init/edgelet.conf
            initctl reload-configuration 2>/dev/null || true
            if [ "${_eng}" = "edgelet" ]; then
                start_edgelet_containerd_unit "${_init}" "${_restart_dp}"
            fi
            initctl restart edgelet 2>/dev/null || initctl start edgelet
            info "Upstart job edgelet installed."
            ;;
        s6)
            mkdir -p /etc/s6/edgelet
            install -m 755 "${_root}/s6/run" /etc/s6/edgelet/run
            install -m 755 "${_root}/s6/finish" /etc/s6/edgelet/finish
            if [ "${_eng}" = "edgelet" ]; then
                start_edgelet_containerd_unit "${_init}" "${_restart_dp}"
            fi
            if command -v s6-svc >/dev/null 2>&1 && [ -d /var/run/s6/services ]; then
                mkdir -p /var/run/s6/services/edgelet
                ln -sf /etc/s6/edgelet /var/run/s6/services/edgelet/supervise 2>/dev/null || true
                s6-svc -d /var/run/s6/services/edgelet 2>/dev/null || true
                s6-svc -u /var/run/s6/services/edgelet 2>/dev/null || true
            fi
            info "s6 service installed under /etc/s6/edgelet (start via your s6 scan)."
            ;;
        runit)
            mkdir -p /etc/runit/edgelet
            install -m 755 "${_root}/runit/run" /etc/runit/edgelet/run
            if [ -f "${_root}/runit/finish" ]; then
                install -m 755 "${_root}/runit/finish" /etc/runit/edgelet/finish
            fi
            if [ "${_eng}" = "edgelet" ]; then
                start_edgelet_containerd_unit "${_init}" "${_restart_dp}"
            fi
            if [ -d /etc/runit ]; then
                ln -sf /etc/runit/edgelet /etc/service/edgelet 2>/dev/null || \
                    ln -sf /etc/runit/edgelet /var/service/edgelet 2>/dev/null || true
                sv restart edgelet 2>/dev/null || sv start edgelet 2>/dev/null || true
            fi
            info "runit service installed under /etc/runit/edgelet."
            ;;
        *)
            die "No supported init system detected (${_init}). Install systemd, procd, openrc, sysvinit, upstart, s6, or runit."
            ;;
    esac
}

stop_edgelet_service() {
    _init="${1:-$(detect_init)}"
    case "${_init}" in
        systemd) systemctl stop edgelet 2>/dev/null || true ;;
        openrc) rc-service edgelet stop 2>/dev/null || true ;;
        procd) /etc/init.d/edgelet stop 2>/dev/null || true ;;
        sysvinit) /etc/init.d/edgelet stop 2>/dev/null || true ;;
        upstart) initctl stop edgelet 2>/dev/null || true ;;
        s6) s6-svc -d /var/run/s6/services/edgelet 2>/dev/null || true ;;
        runit) sv down edgelet 2>/dev/null || true ;;
        *) "${EDGELET_LIBEXEC}/edgelet-shutdown" 2>/dev/null || pkill -f "/usr/local/bin/edgelet daemon" 2>/dev/null || true ;;
    esac
}
