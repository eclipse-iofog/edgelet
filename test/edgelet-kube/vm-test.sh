#!/usr/bin/env bash
# Checks extract, the containerd socket, the bridge conflist, a Ready node,
# the iptables family, and that the Mac can reach the API at the guest address
# on lima0. It then restarts edgelet-kube three times and requires CoreDNS to
# become Ready after each restart.
#
# Usage:
#   ./test/edgelet-kube/vm-test.sh [--vm-name=edgelet-kube-test] [--ready-timeout=1200]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/log.sh
source "${SCRIPT_DIR}/lib/log.sh"

VM_NAME="edgelet-kube-test"
READY_TIMEOUT=1200

for arg in "$@"; do
    case "${arg}" in
        --vm-name=*)       VM_NAME="${arg#*=}" ;;
        --ready-timeout=*) READY_TIMEOUT="${arg#*=}" ;;
        --help|-h)
            sed -n '/^# Usage:/,/^[^#]/p' "$0" | grep '^#' | sed 's/^# \?//'
            exit 0
            ;;
        *) die "unknown option: ${arg}" ;;
    esac
done

refuse_agent_vm "${VM_NAME}"

guest_eval() {
    local payload
    payload="$(printf '%s\n' "$1" | base64 | tr -d '\n')"
    limactl --tty=false shell "${VM_NAME}" -- sudo bash -c "set -o pipefail; printf '%s' '${payload}' | base64 -d | bash"
}

show_diagnostics() {
    log_warn "edgelet-kube.service status:"
    limactl --tty=false shell "${VM_NAME}" -- sudo systemctl status edgelet-kube --no-pager -l || true
    log_warn "recent edgelet-kube logs:"
    limactl --tty=false shell "${VM_NAME}" -- sudo journalctl -u edgelet-kube --no-pager -n 120 || true
}

wait_guest() {
    local desc="$1"
    local timeout="$2"
    local script="$3"
    local elapsed=0
    local next_log=0
    local output=""
    local status=0
    while true; do
        status=0
        output="$(guest_eval "${script}" 2>&1)" || status=$?
        if [[ "${status}" -eq 0 ]]; then
            log_ok "${desc}"
            return 0
        fi
        if (( elapsed >= timeout )); then
            log_fail "${desc} (waited ${timeout}s)"
            if [[ -n "${output}" ]]; then
                printf '%s\n' "${output}" >&2
            fi
            return 1
        fi
        if (( elapsed >= next_log )); then
            log_info "waiting for ${desc} (${elapsed}s/${timeout}s)"
            next_log=$((elapsed + 30))
        fi
        sleep 5
        elapsed=$((elapsed + 5))
    done
}

# lima_guest_ip is the IPv4 address of lima0 inside the guest.
# That is the vzNAT address the Mac can dial. eth0 is the user-mode address.
lima_guest_ip() {
    local line ip
    line="$(guest_eval 'ip -4 -o addr show dev lima0 | awk "{print \$4}" | cut -d/ -f1')"
    ip="$(printf '%s\n' "${line}" | awk 'NF { print; exit }')"
    if [[ ! "${ip}" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
        echo "guest lima0 has no IPv4 address" >&2
        if [[ -n "${line}" ]]; then
            printf '%s\n' "${line}" >&2
        fi
        return 1
    fi
    printf '%s\n' "${ip}"
}

# check_host_api dials the API from the Mac at the guest lima address and
# requires that address on the serving certificate.
check_host_api() {
    local guest san
    guest="$(lima_guest_ip)" || return 1
    if ! nc -z -G 5 "${guest}" 6443; then
        log_fail "API ${guest}:6443 is not reachable from the host"
        return 1
    fi
    san="$(openssl s_client -connect "${guest}:6443" -servername "${guest}" </dev/null 2>/dev/null | openssl x509 -noout -ext subjectAltName 2>/dev/null || true)"
    if ! printf '%s\n' "${san}" | grep -Eq "(IP Address|IP):${guest}(,|[[:space:]]|$)"; then
        log_fail "serving certificate does not include ${guest}"
        printf '%s\n' "${san}" >&2
        return 1
    fi
    log_ok "API https://${guest}:6443 is reachable and the certificate includes ${guest}"
}

EXTRACT_CHECK='set -eu
thin=$(readlink -f /usr/local/bin/edgelet-kube)
fats=$(find /var/lib/edgelet-kube/data -type f -path "*/bin/edgelet-kube" 2>/dev/null || true)
if [ -z "$fats" ]; then
  echo "runtime was not extracted" >&2
  exit 1
fi
count=$(printf "%s\n" "$fats" | grep -c .)
if [ "$count" != "1" ]; then
  echo "expected one extracted runtime" >&2
  printf "%s\n" "$fats" >&2
  exit 1
fi
fat=$(readlink -f "$fats")
test "$thin" != "$fat"
if cmp -s "$thin" "$fat"; then
  echo "installed binary matches the extracted runtime" >&2
  exit 1
fi
pid=$(systemctl show -p MainPID --value edgelet-kube.service)
test "$pid" -gt 0
exe=$(readlink -f "/proc/${pid}/exe")
test "$exe" = "$fat"
child=""
for dir in /proc/[0-9]*; do
  cmd=$(tr "\0" " " < "$dir/cmdline" 2>/dev/null || true)
  case "$cmd" in
    *--edgelet-kube-containerd-child*)
      cexe=$(readlink -f "$dir/exe" 2>/dev/null || true)
      case "$cexe" in
        */bin/edgelet-kube) child=${dir#/proc/}; break ;;
      esac
      ;;
  esac
done
test -n "$child"
cexe=$(readlink -f "/proc/${child}/exe")
test "$cexe" = "$fat"
'

SOCKET_CHECK='set -eu
test -S /run/edgelet-kube/containerd.sock
'

CONFLIST_CHECK='set -eu
file=/var/lib/edgelet-kube/agent/etc/cni/net.d/10-bridge.conflist
test -f "$file"
grep -F -q "\"type\":\"bridge\"" "$file"
grep -F -q "\"hairpinMode\":true" "$file"
'

NODE_CHECK='set -eu
out=$(/usr/local/bin/kubectl --kubeconfig /etc/edgelet-kube/edgelet-kube.yaml get nodes --no-headers)
printf "%s\n" "$out" | awk "{print \$2}" | grep -qx Ready
'

IPTABLES_CHECK='set -eu
aux=$(find /var/lib/edgelet-kube/data -type d -path "*/bin/aux" | head -n 1)
test -n "$aux"
for name in iptables iptables-save iptables-restore ip6tables ip6tables-save ip6tables-restore; do
  test "$(readlink "$aux/$name")" = xtables-nft-multi
done
'

COREDNS_CHECK='set -eu
ready=$(/usr/local/bin/kubectl --kubeconfig /etc/edgelet-kube/edgelet-kube.yaml get pods -n kube-system -l k8s-app=kube-dns -o jsonpath="{.items[0].status.containerStatuses[0].ready}")
test "$ready" = true
'

# Three restarts, each bounded by 90 tries of 2s. CoreDNS stays unready
# until a pod can reach the API. The guest also dials the kubernetes
# Service, which is the host-side check that ClusterIP forwarding is back.
RESTART_CHECK='set -eu
k=/usr/local/bin/kubectl
export KUBECONFIG=/etc/edgelet-kube/edgelet-kube.yaml
portal_open() {
  local svc
  svc=$($k get svc kubernetes -o jsonpath="{.spec.clusterIP}" 2>/dev/null || true)
  test -n "$svc"
  timeout 3 bash -c "echo >/dev/tcp/${svc}/443" 2>/dev/null
}
for i in 1 2 3; do
  echo "restart ${i}/3"
  systemctl restart edgelet-kube
  ok=0
  for _ in $(seq 1 90); do
    if systemctl is-active --quiet edgelet-kube && test -S /run/edgelet-kube/containerd.sock; then
      node=$($k get nodes --no-headers 2>/dev/null | awk "{print \$2}" || true)
      dns=$($k get pods -n kube-system -l k8s-app=kube-dns -o jsonpath="{.items[0].status.containerStatuses[0].ready}" 2>/dev/null || true)
      if [ "$node" = Ready ] && [ "$dns" = true ] && portal_open; then
        ok=1
        break
      fi
    fi
    sleep 2
  done
  test "$ok" = 1
  echo "ready after restart ${i}"
done
'

echo ""
echo "======================================================================"
echo "  edgelet-kube Lima checks"
echo "  VM: ${VM_NAME}"
echo "======================================================================"

log_step "Extracted runtime"
if ! wait_guest "thin binary extracted the fat runtime and execs it" 180 "${EXTRACT_CHECK}"; then
    show_diagnostics
    exit 1
fi

log_step "containerd socket"
if ! wait_guest "containerd socket /run/edgelet-kube/containerd.sock" 180 "${SOCKET_CHECK}"; then
    show_diagnostics
    exit 1
fi

log_step "Bridge conflist"
if ! wait_guest "bridge conflist with hairpinMode true" "${READY_TIMEOUT}" "${CONFLIST_CHECK}"; then
    show_diagnostics
    exit 1
fi

log_step "Node"
if ! wait_guest "node Ready" "${READY_TIMEOUT}" "${NODE_CHECK}"; then
    show_diagnostics
    exit 1
fi

log_step "API from the host"
if ! check_host_api; then
    show_diagnostics
    exit 1
fi

log_step "iptables family"
if ! wait_guest "iptables, iptables-save, and iptables-restore name xtables-nft-multi" 60 "${IPTABLES_CHECK}"; then
    show_diagnostics
    exit 1
fi

log_step "CoreDNS"
if ! wait_guest "CoreDNS Ready" "${READY_TIMEOUT}" "${COREDNS_CHECK}"; then
    show_diagnostics
    exit 1
fi

log_step "Restart storm"
if ! guest_eval "${RESTART_CHECK}"; then
    log_fail "CoreDNS was not Ready after three edgelet-kube restarts"
    show_diagnostics
    exit 1
fi
log_ok "CoreDNS Ready after three edgelet-kube restarts"

log_success "edgelet-kube checks passed"
