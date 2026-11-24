#!/usr/bin/env bash
# Checks extract, the containerd socket, the bridge conflist, a Ready node,
# the iptables family, and that the Mac can reach the API at the guest address
# on lima0. After CoreDNS is Ready it checks placement, then restarts
# edgelet-kube three times and requires CoreDNS to become Ready after each
# restart.
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

# Placement checks use the pause image already imported for the sandbox.
# Each one deletes the pods it creates so a second run starts clean.
read -r -d '' PLACEMENT_PRELUDE <<'EOF' || true
set -eu
set -o pipefail
k=/usr/local/bin/kubectl
export KUBECONFIG=/etc/edgelet-kube/edgelet-kube.yaml
ns=default
pause=docker.io/portainer/pause:latest
node=$(${k} get nodes -o jsonpath='{.items[0].metadata.name}')
test -n "${node}"

delete_pod() {
  if ! ${k} delete pod -n "${ns}" "$1" --ignore-not-found --wait=true --grace-period=0 --force >/dev/null 2>&1; then
    echo "delete pod $1 failed" >&2
    exit 1
  fi
}

pod_node() {
  ${k} get pod -n "${ns}" "$1" -o jsonpath='{.spec.nodeName}' 2>/dev/null || true
}

pod_phase() {
  ${k} get pod -n "${ns}" "$1" -o jsonpath='{.status.phase}' 2>/dev/null || true
}

recent_stamp() {
  printf '%s\n' "$1" | awk -v before="$2" 'NF && ($0 == before || $0 > before) { found = 1 } END { exit found ? 0 : 1 }'
}

wait_running() {
  local name="$1" i phase got
  phase=""
  got=""
  for i in $(seq 1 120); do
    phase=$(pod_phase "${name}")
    got=$(pod_node "${name}")
    if [ "${phase}" = Running ] && [ "${got}" = "${node}" ]; then
      return 0
    fi
    if [ "${phase}" = Failed ] || [ "${phase}" = Succeeded ]; then
      echo "${name} is ${phase}" >&2
      ${k} describe pod -n "${ns}" "${name}" >&2 || true
      exit 1
    fi
    sleep 2
  done
  echo "${name} is ${phase:-unknown} on ${got:-<none>}, want Running on ${node}" >&2
  ${k} describe pod -n "${ns}" "${name}" >&2 || true
  exit 1
}

stay_unbound() {
  local name="$1" i phase got
  phase=""
  got=""
  for i in $(seq 1 5); do
    phase=$(pod_phase "${name}")
    got=$(pod_node "${name}")
    if [ -n "${got}" ]; then
      echo "${name} is bound to ${got}" >&2
      ${k} describe pod -n "${ns}" "${name}" >&2 || true
      exit 1
    fi
    if [ "${phase}" = Failed ] || [ "${phase}" = Succeeded ] || [ "${phase}" = Running ]; then
      echo "${name} is ${phase}" >&2
      ${k} describe pod -n "${ns}" "${name}" >&2 || true
      exit 1
    fi
    sleep 2
  done
  if [ "${phase}" != Pending ]; then
    echo "${name} is ${phase:-unknown}, want Pending" >&2
    ${k} describe pod -n "${ns}" "${name}" >&2 || true
    exit 1
  fi
}
EOF

read -r -d '' SCHEDULER_CHECK <<'EOF' || true
set -eu
for dir in /proc/[0-9]*; do
  comm=$(cat "${dir}/comm" 2>/dev/null || true)
  case "${comm}" in
    kube-scheduler)
      echo "scheduler process ${dir#/proc/}" >&2
      exit 1
      ;;
  esac
  cmd=$({ tr '\0' ' ' < "${dir}/cmdline" || true; } 2>/dev/null)
  set -f
  for word in ${cmd}; do
    case "${word}" in
      kube-scheduler|*/kube-scheduler)
        echo "scheduler process: ${cmd}" >&2
        exit 1
        ;;
    esac
  done
  set +f
done
test -r /proc/net/tcp
nets=/proc/net/tcp
if [ -r /proc/net/tcp6 ]; then
  nets="${nets} /proc/net/tcp6"
fi
# shellcheck disable=SC2086
if awk 'NR > 1 && $4 == "0A" {
  split($2, a, ":")
  if (toupper(a[2]) == "2813") found = 1
}
END { exit found ? 0 : 1 }' ${nets}; then
  echo "something listens on 10259" >&2
  exit 1
fi
EOF

read -r -d '' FREE_POD_CHECK <<'EOF' || true
${k} uncordon "${node}" >/dev/null
delete_pod place-free
${k} apply -n "${ns}" -f - <<YAML
apiVersion: v1
kind: Pod
metadata:
  name: place-free
  namespace: ${ns}
spec:
  restartPolicy: Always
  containers:
  - name: pause
    image: ${pause}
    imagePullPolicy: IfNotPresent
YAML
wait_running place-free
EOF

read -r -d '' SELECTOR_CHECK <<'EOF' || true
delete_pod place-selector
before=$(date -u +%Y-%m-%dT%H:%M:%SZ)
${k} apply -n "${ns}" -f - <<YAML
apiVersion: v1
kind: Pod
metadata:
  name: place-selector
  namespace: ${ns}
spec:
  restartPolicy: Always
  nodeSelector:
    kubernetes.io/hostname: not-this-node
  containers:
  - name: pause
    image: ${pause}
    imagePullPolicy: IfNotPresent
YAML
found=0
for _ in $(seq 1 30); do
  got=$(pod_node place-selector)
  if [ -n "${got}" ]; then
    echo "place-selector is bound to ${got}" >&2
    exit 1
  fi
  stamps=$(${k} get events -n "${ns}" --field-selector involvedObject.name=place-selector,reason=NodeSelector -o jsonpath='{range .items[*]}{.lastTimestamp}{"\n"}{end}' 2>/dev/null || true)
  if recent_stamp "${stamps}" "${before}"; then
    found=1
    break
  fi
  sleep 2
done
if [ "${found}" != 1 ]; then
  echo "place-selector has no NodeSelector event" >&2
  ${k} get events -n "${ns}" --field-selector involvedObject.name=place-selector >&2 || true
  exit 1
fi
stay_unbound place-selector
EOF

read -r -d '' CORDON_CHECK <<'EOF' || true
uncordon_node() { ${k} uncordon "${node}" >/dev/null || true; }
trap uncordon_node EXIT
uncordon_node
delete_pod place-cordon
${k} cordon "${node}" >/dev/null
${k} apply -n "${ns}" -f - <<YAML
apiVersion: v1
kind: Pod
metadata:
  name: place-cordon
  namespace: ${ns}
spec:
  restartPolicy: Always
  containers:
  - name: pause
    image: ${pause}
    imagePullPolicy: IfNotPresent
YAML
stay_unbound place-cordon
${k} uncordon "${node}" >/dev/null
wait_running place-cordon
trap - EXIT
EOF

read -r -d '' HOST_PORT_CHECK <<'EOF' || true
${k} uncordon "${node}" >/dev/null
delete_pod place-port-a
delete_pod place-port-b
sleep 2
${k} apply -n "${ns}" -f - <<YAML
apiVersion: v1
kind: Pod
metadata:
  name: place-port-a
  namespace: ${ns}
spec:
  restartPolicy: Always
  containers:
  - name: pause
    image: ${pause}
    imagePullPolicy: IfNotPresent
    ports:
    - name: http
      containerPort: 18080
      hostPort: 18080
      protocol: TCP
YAML
wait_running place-port-a
${k} apply -n "${ns}" -f - <<YAML
apiVersion: v1
kind: Pod
metadata:
  name: place-port-b
  namespace: ${ns}
spec:
  restartPolicy: Always
  containers:
  - name: pause
    image: ${pause}
    imagePullPolicy: IfNotPresent
    ports:
    - name: http
      containerPort: 18080
      hostPort: 18080
      protocol: TCP
YAML
stay_unbound place-port-b
test "$(pod_phase place-port-a)" = Running
test "$(pod_node place-port-a)" = "${node}"
EOF

read -r -d '' VOLUME_CHECK <<'EOF' || true
${k} uncordon "${node}" >/dev/null
delete_pod place-volume
${k} delete pvc -n "${ns}" place-volume --ignore-not-found --wait=true --timeout=180s >/dev/null
${k} apply -n "${ns}" -f - <<YAML
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: place-volume
  namespace: ${ns}
spec:
  accessModes:
  - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
  storageClassName: local-path
---
apiVersion: v1
kind: Pod
metadata:
  name: place-volume
  namespace: ${ns}
spec:
  restartPolicy: Always
  containers:
  - name: pause
    image: ${pause}
    imagePullPolicy: IfNotPresent
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: place-volume
YAML
phase=""
got=""
ann=""
for _ in $(seq 1 150); do
  phase=$(pod_phase place-volume)
  got=$(pod_node place-volume)
  ann=$(${k} get pvc -n "${ns}" place-volume -o go-template='{{index .metadata.annotations "volume.kubernetes.io/selected-node"}}' 2>/dev/null || true)
  case "${ann}" in
    "<no value>") ann="" ;;
  esac
  if [ "${phase}" = Running ] && [ "${got}" = "${node}" ] && [ "${ann}" = "${node}" ]; then
    exit 0
  fi
  if [ "${phase}" = Failed ]; then
    echo "place-volume failed" >&2
    ${k} describe pod -n "${ns}" place-volume >&2 || true
    ${k} describe pvc -n "${ns}" place-volume >&2 || true
    exit 1
  fi
  sleep 2
done
echo "place-volume is ${phase:-unknown} on ${got:-<none>}, selected node ${ann:-<none>}" >&2
${k} describe pod -n "${ns}" place-volume >&2 || true
${k} describe pvc -n "${ns}" place-volume >&2 || true
exit 1
EOF

read -r -d '' FOREIGN_SCHEDULER_CHECK <<'EOF' || true
delete_pod place-foreign
before=$(date -u +%Y-%m-%dT%H:%M:%SZ)
${k} apply -n "${ns}" -f - <<YAML
apiVersion: v1
kind: Pod
metadata:
  name: place-foreign
  namespace: ${ns}
spec:
  restartPolicy: Always
  schedulerName: custom-scheduler
  containers:
  - name: pause
    image: ${pause}
    imagePullPolicy: IfNotPresent
YAML
stay_unbound place-foreign
lines=$(${k} get events -n "${ns}" --field-selector involvedObject.name=place-foreign -o jsonpath='{range .items[*]}{.reportingController}{" "}{.lastTimestamp}{"\n"}{end}' 2>/dev/null || true)
if printf '%s\n' "${lines}" | awk -v before="${before}" '$1 == "nodeplace" && ($2 == before || $2 > before) { found = 1 } END { exit found ? 0 : 1 }'; then
  echo "place-foreign has a placement event" >&2
  exit 1
fi
EOF

placement_eval() {
    guest_eval "${PLACEMENT_PRELUDE}"$'\n'"$1"
}

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

log_step "Scheduler"
if ! guest_eval "${SCHEDULER_CHECK}"; then
    log_fail "a scheduler process is running or something listens on 10259"
    show_diagnostics
    exit 1
fi
log_ok "no scheduler process and nothing listens on 10259"

log_step "Unconstrained pod"
if ! placement_eval "${FREE_POD_CHECK}"; then
    log_fail "unconstrained pod did not run on this node"
    show_diagnostics
    exit 1
fi
log_ok "unconstrained pod is Running on this node"

log_step "Node selector"
if ! placement_eval "${SELECTOR_CHECK}"; then
    log_fail "node selector for another hostname did not stay Pending"
    show_diagnostics
    exit 1
fi
log_ok "node selector for another hostname stays Pending"

log_step "Cordon"
if ! placement_eval "${CORDON_CHECK}"; then
    log_fail "cordon did not leave the pod unbound, or uncordon did not let it run"
    show_diagnostics
    exit 1
fi
log_ok "cordon leaves a pod unbound and uncordon lets it run"

log_step "Host port"
if ! placement_eval "${HOST_PORT_CHECK}"; then
    log_fail "host port conflict did not leave one pod Running and the other Pending"
    show_diagnostics
    exit 1
fi
log_ok "one host-port pod is Running and the other stays Pending"

log_step "Local path"
if ! placement_eval "${VOLUME_CHECK}"; then
    log_fail "local-path pod did not run with volume.kubernetes.io/selected-node set"
    show_diagnostics
    exit 1
fi
log_ok "local-path pod is Running and the claim selects this node"

log_step "Other scheduler"
if ! placement_eval "${FOREIGN_SCHEDULER_CHECK}"; then
    log_fail "pod with another scheduler name did not stay unbound"
    show_diagnostics
    exit 1
fi
log_ok "pod with another scheduler name stays unbound"

log_step "Restart storm"
if ! guest_eval "${RESTART_CHECK}"; then
    log_fail "CoreDNS was not Ready after three edgelet-kube restarts"
    show_diagnostics
    exit 1
fi
log_ok "CoreDNS Ready after three edgelet-kube restarts"

log_success "edgelet-kube checks passed"
