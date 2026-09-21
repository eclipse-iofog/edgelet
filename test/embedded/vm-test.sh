#!/usr/bin/env bash
# test/embedded/vm-test.sh
#
# Runs the full embedded-containerd integration test suite inside the Lima VM.
# Tests are grouped into 12 phases:
#
#   Phase 1 — Extracted embedded binaries
#   Phase 2 — containerd socket & health
#   Phase 3 — CNI network configuration
#   Phase 4 — EdgeletAPI v1 + CLI microservice operations
#   Phase 5 — Runtime prerequisites
#   Phase 6 — CLI integration
#   Phase 7 — Chaos gates (control restart; data plane stays up — runtime split)
#   Phase 8 — RuntimeClass dual-shim (shim discovery + catalog data-plane restart storm)
#   Phase 9 — Built-in registries + tiny HF and OCI model pulls
#   Phase 10 — Catalog bind + expanded container fields (keeps Phase 9 models until cleanup)
#   Phase 11 — Persistent VOLUME scope, retain across restart/prune, explicit reclaim
#   Phase 12 — Tiny HF Knowledge pull + catalog bind (dataset API; models store untouched)
#
# Usage:
#   ./test/embedded/vm-test.sh [--vm-name=iofog-test]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/log.sh"

VM_NAME="iofog-test"
for arg in "$@"; do
    case "${arg}" in --vm-name=*) VM_NAME="${arg#*=}" ;; esac
done

# Shorthand for running commands in VM as root.
# Pipe via stdin to avoid Lima's 9p mount caching — never use file-path args.
R() { echo "$*" | limactl --tty=false shell "${VM_NAME}" -- sudo bash; }
# Run as non-root (for CLI tests)
U() { echo "$*" | limactl --tty=false shell "${VM_NAME}" -- bash; }

echo ""
echo "======================================================================"
echo "  Edgelet Embedded Containerd Integration Tests"
echo "  VM: ${VM_NAME}"
echo "======================================================================"

###############################################################################
# Phase 1 — Extracted embedded binaries
###############################################################################
log_step "Phase 1: Extracted embedded binaries (thin → fat dispatch)"

assert_ok "thin wrapper installed at /usr/local/bin/edgelet" \
    R "test -x /usr/local/bin/edgelet"

assert_ok "fat runtime extracted to data/current/bin/edgelet" \
    R "test -x /var/lib/edgelet/data/current/bin/edgelet"

assert_ok "thin wrapper and fat runtime are distinct paths" \
    R "set -e
thin=\$(readlink -f /usr/local/bin/edgelet)
fat=\$(readlink -f /var/lib/edgelet/data/current/bin/edgelet)
test \"\${thin}\" != \"\${fat}\""

assert_ok "containerd child re-execs extracted fat runtime" \
    R "set -e
child=\$(pgrep -f -- '--edgelet-containerd-child' | head -n1)
test -n \"\${child}\"
exe=\$(readlink -f /proc/\${child}/exe)
echo \"\${exe}\" | grep -Eq '/var/lib/edgelet/data/[^/]+/bin/edgelet\$'"

assert_ok "containerd-shim-runc-v2 extracted" \
    R "test -x /var/lib/edgelet-containerd/bin/containerd-shim-runc-v2"

assert_ok "crun extracted" \
    R "test -x /var/lib/edgelet-containerd/bin/crun"

assert_ok "CNI bridge plugin extracted" \
    R "test -x /var/lib/edgelet-containerd/cni/plugins/bridge"

assert_ok "CNI host-local plugin extracted" \
    R "test -x /var/lib/edgelet-containerd/cni/plugins/host-local"

assert_ok "CNI portmap plugin extracted" \
    R "test -x /var/lib/edgelet-containerd/cni/plugins/portmap"

assert_ok "CNI loopback plugin extracted" \
    R "test -x /var/lib/edgelet-containerd/cni/plugins/loopback"

###############################################################################
# Phase 2 — containerd socket & health
###############################################################################
log_step "Phase 2: containerd socket and health"

assert_ok "containerd config.toml written" \
    R "test -f /var/lib/edgelet-containerd/config.toml"

assert_ok "containerd config has edgelet socket" \
    R "grep -q '/run/edgelet/containerd.sock' /var/lib/edgelet-containerd/config.toml"

assert_ok "containerd socket exists" \
    R "test -S /run/edgelet/containerd.sock"

assert_ok "edgelet-containerd service active (runtime split)" \
    R "systemctl is-active --quiet edgelet-containerd"

assert_ok "edgelet service is active with EdgeletAPI unix socket" \
    R "systemctl is-active --quiet edgelet && test -S /run/edgelet/edgelet.sock"

assert_contains "system status endpoint is reachable" "edgeletDaemon" \
    R "edgelet system status"

assert_contains "status exposes cgroup mode" "cgroupMode" \
    R "edgelet system status -o json"

assert_ok "cgroup policy matches runtime split (systemd data plane)" \
    R "set -e
systemctl is-active --quiet edgelet-containerd
grep -q 'SystemdCgroup = false' /var/lib/edgelet-containerd/config.toml
! grep -q 'path = \"/edgelet/agent/containerd\"' /var/lib/edgelet-containerd/config.toml
ctd_pid=\$(systemctl show edgelet-containerd -p MainPID --value)
test -n \"\${ctd_pid}\"
echo \"\$(cat /proc/\${ctd_pid}/cgroup | sed -n 's/^0:://p')\" | grep -q 'edgelet-containerd.service'
ctl_pid=\$(systemctl show edgelet -p MainPID --value)
test -n \"\${ctl_pid}\"
echo \"\$(cat /proc/\${ctl_pid}/cgroup | sed -n 's/^0:://p')\" | grep -q 'edgelet.service'"

assert_ok "control plane status reports host cgroup policy (split)" \
    R "set -e
status=\$(edgelet system status 2>/dev/null)
mode=\$(echo \"\${status}\" | sed -n 's/^cgroupMode: //p')
driver=\$(echo \"\${status}\" | sed -n 's/^cgroupDriver: //p')
nested=\$(echo \"\${status}\" | sed -n 's/^cgroupNested: //p')
case \"\${mode}\" in v2|v1|hybrid) ;; *) echo \"unexpected cgroupMode=\${mode}\"; exit 1 ;; esac
case \"\${driver}\" in systemd|cgroupfs) ;; *) echo \"unexpected cgroupDriver=\${driver}\"; exit 1 ;; esac
test \"\${nested}\" = false
journalctl -u edgelet-containerd --no-pager | grep -q 'cgroup mode=v2 driver=systemd' || \
  journalctl -u edgelet-containerd --no-pager | grep -Eq 'cgroup mode=(v1|hybrid) driver='"

assert_ok "runtime.engineReady before deploy-heavy phases" \
    R "set -e
_elapsed=0
while [ \${_elapsed} -lt 180 ]; do
  if edgelet system status 2>/dev/null | grep -q 'runtime.engineReady: true'; then
    exit 0
  fi
  sleep 2
  _elapsed=\$((_elapsed + 2))
done
echo 'runtime.engineReady not true after 180s' >&2
edgelet system status 2>/dev/null | grep -E 'runtime.engineReady:|runtime.agentPhase:' || true
exit 1"

###############################################################################
# Phase 3 — CNI network configuration
###############################################################################
log_step "Phase 3: CNI network configuration"

assert_ok "Canonical CNI conflist written at conf_dir root" \
    R "test -f /var/lib/edgelet-containerd/cni/conf/10-edgelet.conflist"

assert_ok "Canonical CNI conflist has edgelet network name" \
    R "grep -q '\"name\": \"edgelet\"' /var/lib/edgelet-containerd/cni/conf/10-edgelet.conflist"

assert_ok "Canonical CNI conflist has bridge plugin" \
    R "grep -q '\"type\": \"bridge\"' /var/lib/edgelet-containerd/cni/conf/10-edgelet.conflist"

assert_ok "Canonical CNI conflist has bridge name edgelet0" \
    R "grep -q '\"bridge\": \"edgelet0\"' /var/lib/edgelet-containerd/cni/conf/10-edgelet.conflist"

assert_ok "Canonical CNI conflist has portmap plugin" \
    R "grep -q '\"type\": \"portmap\"' /var/lib/edgelet-containerd/cni/conf/10-edgelet.conflist"

assert_ok "Local CNI conflist not active in single-bridge mode" \
    R "test ! -f /var/lib/edgelet-containerd/cni/conf/local/11-edgelet-local.conflist"

assert_ok "Scope selector CNI conflist not active in single-bridge mode" \
    R "test ! -f /var/lib/edgelet-containerd/cni/conf/00-edgelet-scope.conflist"

assert_ok "Managed CNI symlink created in system CNI dir" \
    R "test -L /etc/cni/net.d/10-edgelet.conflist"

assert_ok "Scope selector symlink removed in system CNI dir" \
    R "test ! -L /etc/cni/net.d/00-edgelet-scope.conflist"

assert_ok "Legacy local CNI symlink removed" \
    R "test ! -L /etc/cni/net.d/11-edgelet-local.conflist"

assert_ok "containerd config keeps canonical crun runtime only" \
    R "set -e
grep -q 'runtimes.crun]' /var/lib/edgelet-containerd/config.toml
! grep -q 'runtimes.crun-local]' /var/lib/edgelet-containerd/config.toml"

assert_ok "containerd config accepts iofog.network pod annotation" \
    R "grep -q 'pod_annotations = \\[\"iofog.network\"\\]' /var/lib/edgelet-containerd/config.toml"

###############################################################################
# Phase 4 — LocalAPI v1 + CLI microservice operations
###############################################################################
log_step "Phase 4: EdgeletAPI v1 and CLI operations"

assert_ok "ms ls is reachable (table or empty state)" \
    R "set -e
out=\$(edgelet ms ls || true)
echo \"\${out}\" | grep -Eq 'MICROSERVICENAME|No microservices found.'"

assert_contains "auth whoami returns claims payload" "\"claims\"" \
    R "edgelet auth whoami"

assert_ok "seed/list local registries before deploy tests" \
    R "edgelet registry ls"

assert_ok "create temporary local deploy manifest" \
    R "cat >/tmp/iofog-local-ms.yaml <<'EOF'
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: local-test-ms
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
  schedule: 50
EOF"

assert_contains "deploy -f submits manifest via CLI" "microservice manifest applied successfully" \
    R "edgelet deploy -f /tmp/iofog-local-ms.yaml"

assert_ok "create DNS probe workload A manifest" \
    R "cat >/tmp/iofog-local-dns-a.yaml <<'EOF'
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: local-dns-a
spec:
  image: docker.io/library/busybox:1.36
  registry: 1
  container:
    hostNetworkMode: false
    isPrivileged: false
    commands:
      - /bin/sh
      - -lc
      - sleep 1200
  schedule: 50
EOF"

assert_ok "create DNS probe workload B manifest" \
    R "cat >/tmp/iofog-local-dns-b.yaml <<'EOF'
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: local-dns-b
spec:
  image: docker.io/library/busybox:1.36
  registry: 1
  container:
    hostNetworkMode: false
    isPrivileged: false
    commands:
      - /bin/sh
      - -lc
      - sleep 1200
  schedule: 50
EOF"

assert_contains "deploy DNS probe workload A" "microservice manifest applied successfully" \
    R "edgelet deploy -f /tmp/iofog-local-dns-a.yaml"

assert_contains "deploy DNS probe workload B" "microservice manifest applied successfully" \
    R "edgelet deploy -f /tmp/iofog-local-dns-b.yaml"

assert_ok "discover DNS probe UUID selectors" \
    R "set -e
for i in \$(seq 1 30); do
  ps_out=\$(edgelet ms ls || true)
  dns_a_uuid=\$(echo \"\${ps_out}\" | awk '\$3==\"local-dns-a\"{print \$1; exit}')
  dns_b_uuid=\$(echo \"\${ps_out}\" | awk '\$3==\"local-dns-b\"{print \$1; exit}')
  if [ -n \"\${dns_a_uuid}\" ] && [ -n \"\${dns_b_uuid}\" ]; then
    cat >/tmp/pr6-dns-uuids.env <<EOF
DNS_A_UUID=\${dns_a_uuid}
DNS_B_UUID=\${dns_b_uuid}
EOF
    exit 0
  fi
  sleep 2
done
exit 1"

assert_ok "local workloads are attached to canonical single-bridge CIDR (172.18.x.x)" \
    R "set -e
source /tmp/pr6-dns-uuids.env
inspect_a=\$(edgelet ms inspect \"\${DNS_A_UUID}\")
inspect_b=\$(edgelet ms inspect \"\${DNS_B_UUID}\")
echo \"\${inspect_a}\" | grep -Eq '\"iofog-ip\": \"172\\.18\\.'
echo \"\${inspect_b}\" | grep -Eq '\"iofog-ip\": \"172\\.18\\.'"

assert_ok "local DNS probe resolves peer from inside container" \
    R "set -e
source /tmp/pr6-dns-uuids.env
for i in \$(seq 1 20); do
  if edgelet ms exec \"\${DNS_A_UUID}\" -- nslookup edgelet.local-dns-b >/dev/null 2>&1; then
    exit 0
  fi
  sleep 3
done
exit 1"

assert_ok "reserved agent alias resolves from local workload" \
    R "set -e
source /tmp/pr6-dns-uuids.env
edgelet ms exec \"\${DNS_A_UUID}\" -- nslookup edgelet.default.svc.bridge.local >/dev/null 2>&1"

assert_ok "status exposes DNS operability fields" \
    R "set -e
out=\$(edgelet system status)
echo \"\${out}\" | grep -q 'dnsHealth'
echo \"\${out}\" | grep -q 'dnsScopeManagedListening'
echo \"\${out}\" | grep -q 'dnsRateLimitEnabled'"

assert_ok "metrics endpoint exposes DNS series" \
    R "set -e
if command -v curl >/dev/null 2>&1; then
  metrics=\$(curl -ksSf https://127.0.0.1:54321/metrics || curl --unix-socket /var/run/edgelet.sock -sSf http://localhost/metrics)
  echo \"\${metrics}\" | grep -q 'iofog_dns_queries_total'
  echo \"\${metrics}\" | grep -q 'iofog_dns_forwarding_degraded'
  echo \"\${metrics}\" | grep -q 'iofog_dns_rate_limited_total'
else
  exit 1
fi"

assert_ok "airgapped forward-fail keeps internal local resolution working" \
    R "set -e
source /tmp/pr6-dns-uuids.env
orig_target=\$(readlink -f /etc/resolv.conf)
cp -a \"\${orig_target}\" /tmp/resolv.conf.pr6.bak
trap 'cat /tmp/resolv.conf.pr6.bak > \"\${orig_target}\"; rm -f /tmp/resolv.conf.pr6.bak' EXIT
before=\$(edgelet system status)
before_q=\$(echo \"\${before}\" | awk -F': ' '/dnsQueriesTotal/{print \$2}')
before_succ=\$(echo \"\${before}\" | awk -F': ' '/dnsSuccessTotal/{print \$2}')
before_ferr=\$(echo \"\${before}\" | awk -F': ' '/dnsForwardErrTotal/{print \$2}')
before_srv=\$(echo \"\${before}\" | awk -F': ' '/dnsServFailTotal/{print \$2}')
printf 'nameserver 203.0.113.1\noptions timeout:1 attempts:1\n' >\"\${orig_target}\"
set +e
edgelet ms exec \"\${DNS_A_UUID}\" -- nslookup example.com >/tmp/pr6-airgap-external.out 2>&1
edgelet ms exec \"\${DNS_A_UUID}\" -- nslookup edgelet.local-dns-b >/tmp/pr6-airgap-internal.out 2>&1
set -e
after=\$(edgelet system status)
after_q=\$(echo \"\${after}\" | awk -F': ' '/dnsQueriesTotal/{print \$2}')
after_succ=\$(echo \"\${after}\" | awk -F': ' '/dnsSuccessTotal/{print \$2}')
after_ferr=\$(echo \"\${after}\" | awk -F': ' '/dnsForwardErrTotal/{print \$2}')
after_srv=\$(echo \"\${after}\" | awk -F': ' '/dnsServFailTotal/{print \$2}')
test \"\${after_q}\" -gt \"\${before_q}\"
test \"\${after_succ}\" -gt \"\${before_succ}\"
test \"\${after_ferr}\" -gt \"\${before_ferr}\"
test \"\${after_srv}\" -gt \"\${before_srv}\"
echo \"\${after}\" | grep -q 'dnsForwardingDegraded: true'
cat /tmp/resolv.conf.pr6.bak > \"\${orig_target}\"
rm -f /tmp/resolv.conf.pr6.bak
trap - EXIT"

###############################################################################
# Phase 5 — Runtime prerequisites
###############################################################################
log_step "Phase 5: Runtime prerequisites"

# Check IP forwarding is enabled
assert_ok "IP forwarding enabled" \
    R "cat /proc/sys/net/ipv4/ip_forward | grep -q 1"

# Check crun is functional
assert_ok "crun is executable and reports version" \
    R "/var/lib/edgelet-containerd/bin/crun --version"

###############################################################################
# Phase 6 — CLI integration
###############################################################################
log_step "Phase 6: CLI integration"

assert_ok "edgelet binary is executable" \
    R "test -x /usr/local/bin/edgelet"

assert_contains "thin CLI version shows embedded engine without daemon extract" "embedded engine: true" \
    R "edgelet version"

assert_contains "thin CLI version lists allowed containerEngine values" "allowed containerEngine: edgelet,docker,podman" \
    R "edgelet version"

assert_contains "system info shows containerEngine=edgelet" "edgelet" \
    R "edgelet system info 2>/dev/null || edgelet system info"

# assert_ok "edgelet config containerEngine iofog accepted" \
#     R "edgelet config -ce iofog"

# assert_contains "system info shows containerEngine=edgelet" "iofog" \
#     R "edgelet system info 2>/dev/null || edgelet system info"

###############################################################################
# Phase 7 — Chaos gates (restart storm + crash injection)
###############################################################################
log_step "Phase 7: Chaos gates"

assert_ok "restart storm converges across 10 systemctl restart cycles" \
    R "set -e
for i in \$(seq 1 10); do
  start_ts=\$(date +%s)
  systemctl restart edgelet
  ok=0
  for j in \$(seq 1 60); do
    if systemctl is-active --quiet edgelet \
       && systemctl is-active --quiet edgelet-containerd \
       && test -S /run/edgelet/edgelet.sock \
       && test -S /run/edgelet/containerd.sock \
       && edgelet system status >/dev/null 2>&1; then
      ok=1
      break
    fi
    sleep 1
  done
  test \"\${ok}\" -eq 1
  elapsed=\$(( \$(date +%s) - start_ts ))
  # Keep restart bounded so lingering shims cannot hold the unit in final-sigterm for minutes.
  test \"\${elapsed}\" -le 75
done"

assert_ok "service leaves deactivating state after restart storm" \
    R "set -e
sub_state=\$(systemctl show -p SubState --value edgelet)
test \"\${sub_state}\" != \"deactivating\""

assert_ok "journald has no forbidden startup signatures" \
    R "set -e
! journalctl -u edgelet -n 800 --no-pager | grep -Eqi 'text file busy|ETXTBSY|Start request repeated too quickly'"

assert_ok "runtime child crash recovers within bounded window" \
    R "set -e
# Runtime split: child is owned by edgelet-containerd; recovery is a unit cycle (drain may take minutes).
data_plane_ready() {
  systemctl is-active --quiet edgelet-containerd \
    && test -S /run/edgelet/containerd.sock \
    && pgrep -f -- '--edgelet-containerd-child' >/dev/null
}
control_ready() {
  systemctl is-active --quiet edgelet \
    && test -S /run/edgelet/edgelet.sock \
    && edgelet system status >/dev/null 2>&1
}
old_child=\$(pgrep -f -- '--edgelet-containerd-child' | head -n1 || true)
test -n \"\${old_child}\"
kill -9 \"\${old_child}\" || true
ok=0
for i in \$(seq 1 90); do
  if data_plane_ready; then ok=1; break; fi
  sleep 2
done
if [ \"\${ok}\" -ne 1 ]; then
  systemctl restart edgelet-containerd
  ok=0
  for i in \$(seq 1 120); do
    if data_plane_ready; then ok=1; break; fi
    sleep 2
  done
fi
test \"\${ok}\" -eq 1
ok=0
for i in \$(seq 1 60); do
  if control_ready; then ok=1; break; fi
  sleep 2
done
test \"\${ok}\" -eq 1"

assert_ok "DNS convergence remains healthy after restart storm and crash recovery" \
    R "set -e
systemctl is-active --quiet edgelet-containerd
test -S /run/edgelet/containerd.sock
systemctl is-active --quiet edgelet
test -S /run/edgelet/edgelet.sock
edgelet system status >/dev/null 2>&1
source /tmp/pr6-dns-uuids.env
ok=0
for i in \$(seq 1 60); do
  if edgelet ms exec \"\${DNS_A_UUID}\" -- nslookup edgelet.local-dns-b >/dev/null 2>&1; then
    ok=1
    break
  fi
  sleep 2
done
test \"\${ok}\" -eq 1
for i in \$(seq 1 10); do
  edgelet ms exec \"\${DNS_A_UUID}\" -- nslookup edgelet.local-dns-b >/dev/null 2>&1
done
status=\$(edgelet system status)
echo \"\${status}\" | grep -q 'dnsHealth: ready\\|dnsHealth: degraded'
echo \"\${status}\" | grep -q 'dnsForwardErrTotal'"

###############################################################################
# Summary
###############################################################################
###############################################################################
# Phase 8 — RuntimeClass dual-shim activation (Spin + Edgelet)
###############################################################################
log_step "Phase 8: RuntimeClass dual-shim activation"

assert_ok "download and install Spin + Edgelet shim binaries (aarch64)" \
    R "set -e
shim_dir=/tmp/runtimeclass-shims
rm -rf \"\${shim_dir}\"
mkdir -p \"\${shim_dir}\"

spin_url='https://github.com/spinframework/containerd-shim-spin/releases/download/v0.24.0/containerd-shim-spin-v2-linux-aarch64.tar.gz'
edgelet_url='https://github.com/Datasance/containerd-shim-edgelet/releases/download/v0.1.0/containerd-shim-edgelet-wasm-v2-aarch64-linux-gnu.tar.gz'

curl -fsSL \"\${spin_url}\" -o \"\${shim_dir}/spin.tar.gz\"
curl -fsSL \"\${edgelet_url}\" -o \"\${shim_dir}/edgelet.tar.gz\"

mkdir -p \"\${shim_dir}/spin\" \"\${shim_dir}/edgelet\"
tar -xzf \"\${shim_dir}/spin.tar.gz\" -C \"\${shim_dir}/spin\"
tar -xzf \"\${shim_dir}/edgelet.tar.gz\" -C \"\${shim_dir}/edgelet\"

spin_bin=\$(find \"\${shim_dir}/spin\" -type f -name 'containerd-shim-spin*' | head -n1)
edgelet_bin=\$(find \"\${shim_dir}/edgelet\" -type f -name 'containerd-shim-edgelet-wasm-v2*' | head -n1)
test -n \"\${spin_bin}\"
test -n \"\${edgelet_bin}\"

install -m 0755 \"\${spin_bin}\" /usr/local/bin/containerd-shim-spin-v2
install -m 0755 \"\${edgelet_bin}\" /usr/local/bin/containerd-shim-edgelet-v2
test -x /usr/local/bin/containerd-shim-spin-v2
test -x /usr/local/bin/containerd-shim-edgelet-v2"

assert_ok "data-plane restart after shim install (edgelet-containerd)" \
    R "set -e
systemctl reset-failed edgelet-containerd 2>/dev/null || true
systemctl restart edgelet-containerd
ok=0
for i in \$(seq 1 120); do
  if systemctl is-active --quiet edgelet-containerd \
     && test -S /run/edgelet/containerd.sock \
     && pgrep -f -- '--edgelet-containerd-child' >/dev/null; then
    ok=1
    break
  fi
  sleep 2
done
test \"\${ok}\" -eq 1"

assert_ok "config.toml lists installed spin and edgelet shims" \
    R "set -e
grep -q 'runtimes.spin]' /var/lib/edgelet-containerd/config.toml
grep -q 'containerd-shim-spin-v2' /var/lib/edgelet-containerd/config.toml
grep -q 'runtimes.edgelet-wasmtime]' /var/lib/edgelet-containerd/config.toml
grep -q 'containerd-shim-edgelet-v2' /var/lib/edgelet-containerd/config.toml"

assert_ok "control reattach after data-plane restart (edgelet)" \
    R "set -e
systemctl restart edgelet
ok=0
for i in \$(seq 1 60); do
  if systemctl is-active --quiet edgelet \
     && test -S /run/edgelet/edgelet.sock \
     && edgelet system status >/dev/null 2>&1; then
    ok=1
    break
  fi
  sleep 2
done
test \"\${ok}\" -eq 1
systemctl is-active --quiet edgelet-containerd"

assert_ok "create RuntimeClass manifest for spin" \
    R "cat >/tmp/runtimeclass-spin.yaml <<'EOF'
apiVersion: edgelet.iofog.org/v1
kind: RuntimeClass
metadata:
  name: spin
handler: spin
EOF"

assert_ok "create RuntimeClass manifest for edgelet-wasmtime" \
    R "cat >/tmp/runtimeclass-edgelet-wasmtime.yaml <<'EOF'
apiVersion: edgelet.iofog.org/v1
kind: RuntimeClass
metadata:
  name: edgelet-wasmtime
handler: edgelet-wasmtime
EOF"

assert_contains "apply RuntimeClass spin via CLI (DB row)" "runtimeclass manifest applied successfully" \
    R "test -S /run/edgelet/edgelet.sock && edgelet deploy -f /tmp/runtimeclass-spin.yaml"

assert_contains "apply RuntimeClass edgelet-wasmtime via CLI (DB row)" "runtimeclass manifest applied successfully" \
    R "test -S /run/edgelet/edgelet.sock && edgelet deploy -f /tmp/runtimeclass-edgelet-wasmtime.yaml"

assert_ok "runtimeclass ls lists spin and edgelet-wasmtime handlers" \
    R "set -e
out=\$(edgelet runtimeclass ls)
echo \"\${out}\" | grep -q spin
echo \"\${out}\" | grep -q edgelet-wasmtime"

assert_contains "validate RuntimeClass spin manifest" "manifest is valid" \
    R "test -S /run/edgelet/edgelet.sock && edgelet deploy -f /tmp/runtimeclass-spin.yaml --dry-run"

assert_contains "validate RuntimeClass edgelet-wasmtime manifest" "manifest is valid" \
    R "test -S /run/edgelet/edgelet.sock && edgelet deploy -f /tmp/runtimeclass-edgelet-wasmtime.yaml --dry-run"

assert_ok "create RuntimeClass apply/delete operation helper" \
    R "cat >/tmp/runtimeclass-ops.sh <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

runtimeclass_json_field() {
  local body=\"\${1}\" key=\"\${2}\"
  echo \"\${body}\" | sed -n \"s/.*\\\"\${key}\\\":\\\"\\([^\\\"]*\\\)\\\".*/\\1/p\" | head -1
}

runtimeclass_token_file() {
  if [ -f /etc/edgelet/edgelet-api ]; then
    echo /etc/edgelet/edgelet-api
    return 0
  fi
  return 1
}

runtimeclass_api() {
  local method=\"\${1}\"
  local path=\"\${2}\"
  shift 2
  local token_file
  token_file=\$(runtimeclass_token_file)
  local token
  token=\$(tr -d '\\n' <\"\${token_file}\")
  if [ \"\${method}\" = \"GET\" ]; then
    local attempt
    for attempt in \$(seq 1 5); do
      if curl -ksS -X \"\${method}\" \
        -H \"Authorization: Bearer \${token}\" \
        \"\$@\" \
        \"https://127.0.0.1:54321\${path}\"; then
        return 0
      fi
      sleep 1
    done
    return 1
  fi
  curl -ksS -X \"\${method}\" \
    -H \"Authorization: Bearer \${token}\" \
    \"\$@\" \
    \"https://127.0.0.1:54321\${path}\"
}

runtimeclass_wait_operation() {
  local kind=\"\${1}\"
  local operation_id=\"\${2}\"
  local endpoint=\"/v1/deploy/runtimeclasses:\${kind}/\${operation_id}\"
  for i in \$(seq 1 120); do
    body=\$(runtimeclass_api GET \"\${endpoint}\")
    echo \"\${body}\" | grep -q '\"success\":true'
    status=\$(runtimeclass_json_field \"\${body}\" status)
    if [ \"\${status}\" = \"succeeded\" ]; then
      return 0
    fi
    if [ \"\${status}\" = \"failed\" ]; then
      echo \"\${body}\" >&2
      return 1
    fi
    sleep 1
  done
  return 1
}

runtimeclass_apply_wait() {
  local manifest_path=\"\${1}\"
  local response_file
  response_file=\$(mktemp)
  local http_code
  http_code=\$(runtimeclass_api POST /v1/deploy/runtimeclasses:apply \
    -F \"manifest=@\${manifest_path}\" \
    -w '%{http_code}' \
    -o \"\${response_file}\")
  local body
  body=\$(cat \"\${response_file}\")
  rm -f \"\${response_file}\"
  echo \"\${body}\" | grep -q '\"success\":true'
  if [ \"\${http_code}\" = \"200\" ]; then
    status=\$(runtimeclass_json_field \"\${body}\" status)
    if [ \"\${status}\" = \"succeeded\" ]; then
      return 0
    fi
  fi
  if [ \"\${http_code}\" = \"202\" ] || [ \"\${http_code}\" = \"200\" ]; then
    operation_id=\$(runtimeclass_json_field \"\${body}\" operationId)
    test -n \"\${operation_id}\"
    runtimeclass_wait_operation apply \"\${operation_id}\"
    return 0
  fi
  echo \"\${body}\" >&2
  return 1
}

runtimeclass_delete_wait() {
  local runtime_name=\"\${1}\"
  local response_file
  response_file=\$(mktemp)
  local http_code
  http_code=\$(runtimeclass_api DELETE \"/v1/deploy/runtimeclasses/\${runtime_name}\" \
    -w '%{http_code}' \
    -o \"\${response_file}\")
  local body
  body=\$(cat \"\${response_file}\")
  rm -f \"\${response_file}\"
  echo \"\${body}\" | grep -q '\"success\":true'
  if [ \"\${http_code}\" = \"200\" ]; then
    status=\$(runtimeclass_json_field \"\${body}\" status)
    if [ \"\${status}\" = \"succeeded\" ]; then
      return 0
    fi
  fi
  if [ \"\${http_code}\" = \"202\" ] || [ \"\${http_code}\" = \"200\" ]; then
    operation_id=\$(runtimeclass_json_field \"\${body}\" operationId)
    test -n \"\${operation_id}\"
    runtimeclass_wait_operation delete \"\${operation_id}\"
    return 0
  fi
  echo \"\${body}\" >&2
  return 1
}

runtimeclass_delete_expect_in_use() {
  local runtime_name=\"\${1}\"
  local blocking_uuid=\"\${2}\"
  local response_file
  response_file=\$(mktemp)
  local http_code
  http_code=\$(runtimeclass_api DELETE \"/v1/deploy/runtimeclasses/\${runtime_name}\" \
    -w '%{http_code}' \
    -o \"\${response_file}\")
  local body
  body=\$(cat \"\${response_file}\")
  rm -f \"\${response_file}\"
  test \"\${http_code}\" = \"400\"
  echo \"\${body}\" | grep -q '\"success\":false'
  echo \"\${body}\" | grep -q '\"code\":\"INVALID_ARGUMENT\"'
  echo \"\${body}\" | grep -q \"\${blocking_uuid}\"
}

runtimeclass_expect_missing() {
  local runtime_name=\"\${1}\"
  local response_file
  response_file=\$(mktemp)
  local http_code
  http_code=\$(runtimeclass_api GET \"/v1/deploy/runtimeclasses/\${runtime_name}\" \
    -w '%{http_code}' \
    -o \"\${response_file}\")
  local body
  body=\$(cat \"\${response_file}\")
  rm -f \"\${response_file}\"
  test \"\${http_code}\" = \"404\"
  echo \"\${body}\" | grep -q '\"success\":false'
  echo \"\${body}\" | grep -q '\"code\":\"NOT_FOUND\"'
}
EOF
chmod +x /tmp/runtimeclass-ops.sh"

assert_ok "apply RuntimeClass spin succeeds without controlled containerd reconfigure" \
    R "set -e
test -S /run/edgelet/edgelet.sock
source /tmp/runtimeclass-ops.sh
since_ts=\$(date '+%Y-%m-%d %H:%M:%S')
runtimeclass_apply_wait /tmp/runtimeclass-spin.yaml
systemctl is-active --quiet edgelet
edgelet system status >/dev/null 2>&1
! journalctl -u edgelet --since \"\${since_ts}\" --no-pager | grep -q 'Starting controlled embedded containerd reconfigure'"

assert_ok "apply RuntimeClass edgelet-wasmtime succeeds without controlled containerd reconfigure" \
    R "set -e
test -S /run/edgelet/edgelet.sock
source /tmp/runtimeclass-ops.sh
since_ts=\$(date '+%Y-%m-%d %H:%M:%S')
runtimeclass_apply_wait /tmp/runtimeclass-edgelet-wasmtime.yaml
systemctl is-active --quiet edgelet
edgelet system status >/dev/null 2>&1
! journalctl -u edgelet --since \"\${since_ts}\" --no-pager | grep -q 'Starting controlled embedded containerd reconfigure'"

assert_ok "availableRuntimes includes RuntimeClass canonical entries" \
    R "set -e
ok=0
for i in \$(seq 1 60); do
  status=\$(edgelet system status || true)
  if echo \"\${status}\" | grep -q 'availableRuntimes' &&
     echo \"\${status}\" | grep -q 'spin' &&
     echo \"\${status}\" | grep -q 'edgelet-wasmtime' &&
     ! echo \"\${status}\" | grep -q 'spin-local' &&
     ! echo \"\${status}\" | grep -q 'edgelet-wasmtime-local'; then
    ok=1
    break
  fi
  sleep 1
done
test \"\${ok}\" -eq 1"

assert_ok "create runtime-pinned Spin workload manifest" \
    R "cat >/tmp/runtimeclass-ms-spin.yaml <<'EOF'
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: runtime-spin-ms
spec:
  image: ghcr.io/spinframework/containerd-shim-spin/examples/spin-rust-hello:v0.22.0
  registry: 1
  container:
    hostNetworkMode: false
    isPrivileged: false
    platform: wasi/wasm
    runtime: spin
    ports:
      - internal: 80
        external: 8080
        protocol: tcp
    commands:
      - "/"
  schedule: 50
EOF"

assert_ok "create runtime-pinned Edgelet workload manifest" \
    R "cat >/tmp/runtimeclass-ms-edgelet.yaml <<'EOF'
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: runtime-edgelet-ms
spec:
  image: ghcr.io/containerd/runwasi/wasi-demo-app:latest
  registry: 1
  container:
    hostNetworkMode: false
    isPrivileged: false
    platform: wasi/wasm
    runtime: edgelet-wasmtime
  schedule: 50
EOF"

assert_contains "deploy runtime-pinned Spin workload" "microservice manifest applied successfully" \
    R "test -S /run/edgelet/edgelet.sock && edgelet deploy -f /tmp/runtimeclass-ms-spin.yaml"

assert_contains "deploy runtime-pinned Edgelet workload" "microservice manifest applied successfully" \
    R "test -S /run/edgelet/edgelet.sock && edgelet deploy -f /tmp/runtimeclass-ms-edgelet.yaml"

assert_ok "runtime-pinned Spin workload reaches running state" \
    R "set -e
for i in \$(seq 1 60); do
  out=\$(edgelet ms ls || true)
  if echo \"\${out}\" | awk '\$3==\"runtime-spin-ms\" && tolower(\$4)==\"running\" {found=1} END{exit(found?0:1)}'; then
    exit 0
  fi
  sleep 2
done
exit 1"

assert_ok "runtime-pinned Edgelet workload reaches running state" \
    R "set -e
for i in \$(seq 1 60); do
  out=\$(edgelet ms ls || true)
  if echo \"\${out}\" | awk '\$3==\"runtime-edgelet-ms\" && tolower(\$4)==\"running\" {found=1} END{exit(found?0:1)}'; then
    exit 0
  fi
  sleep 2
done
exit 1"

assert_ok "Spin hostport sanity check succeeds on localhost:8080" \
    R "set -e
ok=0
for i in \$(seq 1 40); do
  if curl -fsS --max-time 3 http://127.0.0.1:8080/hello >/tmp/runtimeclass-spin-hostport.out 2>&1; then
    ok=1
    break
  fi
  sleep 2
done
test \"\${ok}\" -eq 1"

assert_ok "DNS sanity after RuntimeClass apply exposes expected listener health fields" \
    R "set -e
status=\$(edgelet system status)
echo \"\${status}\" | grep -q 'dnsHealth:'
echo \"\${status}\" | grep -q 'dnsScopeManagedListening'"

assert_ok "capture runtime-pinned workload UUIDs" \
    R "set -e
spin_uuid=''
edgelet_uuid=''
for i in \$(seq 1 60); do
  out=\$(edgelet ms ls || true)
  spin_uuid=\$(echo \"\${out}\" | awk '\$3==\"runtime-spin-ms\" {print \$1}' | head -n1)
  edgelet_uuid=\$(echo \"\${out}\" | awk '\$3==\"runtime-edgelet-ms\" {print \$1}' | head -n1)
  if [ -n \"\${spin_uuid}\" ] && [ -n \"\${edgelet_uuid}\" ]; then
    break
  fi
  sleep 1
done
test -n \"\${spin_uuid}\"
test -n \"\${edgelet_uuid}\"
printf 'RUNTIME_SPIN_UUID=%s\nRUNTIME_EDGELET_UUID=%s\n' \"\${spin_uuid}\" \"\${edgelet_uuid}\" >/tmp/runtimeclass-ms-uuids.env"

assert_ok "runtimeclass delete is rejected while dependent workload exists" \
    R "set -e
source /tmp/runtimeclass-ops.sh
source /tmp/runtimeclass-ms-uuids.env
runtimeclass_delete_expect_in_use spin \"\${RUNTIME_SPIN_UUID}\""

assert_ok "catalog data-plane restart storm x3 with WASM workloads running" \
    R "set -e
source /tmp/runtimeclass-ms-uuids.env

data_plane_ready() {
  local since_ts=\"\$1\"
  systemctl is-active --quiet edgelet-containerd \
    && test -S /run/edgelet/containerd.sock \
    && pgrep -f 'edgelet-containerd-child' >/dev/null \
    && journalctl -u edgelet-containerd --since \"\${since_ts}\" --no-pager \
       | grep -q 'Embedded containerd is ready'
}

control_ready() {
  systemctl is-active --quiet edgelet \
    && test -S /run/edgelet/edgelet.sock \
    && edgelet system status >/dev/null 2>&1
}

catalog_runtimes_ok() {
  status=\$(edgelet system status || true)
  echo \"\${status}\" | grep -q 'spin' \
    && echo \"\${status}\" | grep -q 'edgelet-wasmtime' \
    && echo \"\${status}\" | grep -q 'runtime.engineReady: true'
}

workloads_running() {
  out=\$(edgelet ms ls || true)
  echo \"\${out}\" | awk '\$3==\"runtime-spin-ms\" && \$5!=\"\" {s=1}
                       \$3==\"runtime-edgelet-ms\" && \$5!=\"\" {e=1}
                       END{exit (s&&e)?0:1}'
  curl -fsS --max-time 5 http://127.0.0.1:8080/hello >/dev/null
}

ok=0
for j in \$(seq 1 30); do
  if control_ready && workloads_running; then
    ok=1
    break
  fi
  sleep 2
done
test \"\${ok}\" -eq 1

for i in \$(seq 1 3); do
  since_ts=\$(date '+%Y-%m-%d %H:%M:%S')
  control_ready
  workloads_running
  systemctl reset-failed edgelet-containerd 2>/dev/null || true
  systemctl restart edgelet-containerd
  ok=0
  for j in \$(seq 1 120); do
    if data_plane_ready \"\${since_ts}\" && catalog_runtimes_ok && control_ready; then
      ok=1
      break
    fi
    sleep 2
  done
  test \"\${ok}\" -eq 1
  journal=\$(journalctl -u edgelet-containerd --since \"\${since_ts}\" --no-pager || true)
  echo \"\${journal}\" | grep -q 'Embedded containerd is ready'
  echo \"\${journal}\" | grep -q 'drain_complete'
  ! echo \"\${journal}\" | grep -q 'drain_degraded'
  ! echo \"\${journal}\" | grep -q 'rename extracted bundle: file exists'
  ! echo \"\${journal}\" | grep -q 'Preparing data dir'
  ok=0
  for j in \$(seq 1 90); do
    if workloads_running; then
      ok=1
      break
    fi
    sleep 2
  done
  test \"\${ok}\" -eq 1
  curl -fsS --max-time 5 http://127.0.0.1:8080/hello >/dev/null
done

readlink /var/lib/edgelet/data/current/bin/aux/iptables | grep -q xtables-legacy-multi
systemctl is-active --quiet edgelet
test -S /run/edgelet/edgelet.sock
edgelet system status >/dev/null 2>&1"

assert_ok "remove runtime-pinned workloads before runtimeclass delete" \
    R "set -e
source /tmp/runtimeclass-ms-uuids.env
edgelet ms rm \"\${RUNTIME_SPIN_UUID}\" >/dev/null
edgelet ms rm \"\${RUNTIME_EDGELET_UUID}\" >/dev/null
ok=0
for i in \$(seq 1 60); do
  out=\$(edgelet ms ls || true)
  if ! echo \"\${out}\" | grep -q 'runtime-spin-ms' &&
     ! echo \"\${out}\" | grep -q 'runtime-edgelet-ms'; then
    ok=1
    break
  fi
  sleep 1
done
test \"\${ok}\" -eq 1"

assert_ok "delete RuntimeClass spin converges (sync or async path)" \
    R "set -e
source /tmp/runtimeclass-ops.sh
runtimeclass_delete_wait spin"

assert_ok "delete RuntimeClass edgelet-wasmtime converges (sync or async path)" \
    R "set -e
source /tmp/runtimeclass-ops.sh
runtimeclass_delete_wait edgelet-wasmtime"

assert_ok "deleted RuntimeClass entries are no longer retrievable via API" \
    R "set -e
source /tmp/runtimeclass-ops.sh
runtimeclass_expect_missing spin
runtimeclass_expect_missing edgelet-wasmtime"

###############################################################################
# Phase 9 — Built-in registries + tiny HF and OCI models
###############################################################################
log_step "Phase 9: Built-in registries and tiny HF / OCI models"

assert_ok "registry ls includes docker.io, from_cache, and Hugging Face Hub" \
    R "set -e
out=\$(edgelet registry ls -o json)
echo \"\${out}\" | grep -q 'docker.io'
echo \"\${out}\" | grep -q 'from_cache'
echo \"\${out}\" | grep -q 'huggingface.co'
echo \"\${out}\" | grep -q '\"type\": \"hf\"'"

assert_ok "refuse remove built-in docker.io registry" \
    R "set -e
out=\$(edgelet registry rm 1 2>&1 || true)
echo \"\${out}\" | grep -q 'cannot be removed'"

assert_ok "refuse remove built-in from_cache registry" \
    R "set -e
out=\$(edgelet registry rm 2 2>&1 || true)
echo \"\${out}\" | grep -q 'cannot be removed'"

assert_ok "refuse remove built-in Hugging Face registry" \
    R "set -e
out=\$(edgelet registry rm 3 2>&1 || true)
echo \"\${out}\" | grep -q 'cannot be removed'"

assert_ok "create built-in registry edit probe manifest" \
    R "cat >/tmp/edgelet-builtin-hf-reg.yaml <<'EOF'
apiVersion: edgelet.iofog.org/v1
kind: Registry
spec:
  id: 3
  type: hf
  url: https://huggingface.co
  private: false
EOF"

assert_ok "refuse deploy edit of built-in Hugging Face registry" \
    R "set -e
out=\$(edgelet deploy -f /tmp/edgelet-builtin-hf-reg.yaml 2>&1 || true)
echo \"\${out}\" | grep -q 'cannot be edited'"

assert_ok "create throwaway user registry manifest" \
    R "cat >/tmp/edgelet-user-reg.yaml <<'EOF'
apiVersion: edgelet.iofog.org/v1
kind: Registry
spec:
  id: 20
  type: oci
  url: registry.it.example
  private: false
EOF"

assert_contains "deploy throwaway user registry" "registry manifest applied successfully" \
    R "edgelet deploy -f /tmp/edgelet-user-reg.yaml"

assert_ok "remove throwaway user registry" \
    R "edgelet registry rm 20"

assert_ok "image pull rejects Hugging Face registry id" \
    R "set -e
out=\$(edgelet image pull docker.io/library/alpine:3.19 -r 3 2>&1 || true)
echo \"\${out}\" | grep -q 'oci'"

assert_ok "create tiny Hugging Face model manifest" \
    R "cat >/tmp/tiny-gpt2.yaml <<'EOF'
apiVersion: edgelet.iofog.org/v1
kind: Model
metadata:
  name: tiny-gpt2
spec:
  repo: hf-internal-testing/tiny-random-gpt2
  revision: 71034c5d8bde858ff824298bdedc65515b97d2b9
  registry: 3
  files:
    - config.json
  format: unknown
EOF"

assert_contains "deploy tiny Hugging Face model" "model manifest applied successfully" \
    R "edgelet deploy -f /tmp/tiny-gpt2.yaml"

assert_contains "model ls lists tiny-gpt2" "tiny-gpt2" \
    R "edgelet model ls"

assert_contains "inspect tiny model is Ready" "Ready" \
    R "edgelet model inspect tiny-gpt2"

assert_ok "tiny Hugging Face model content materialized on disk" \
    R "test -f /var/lib/edgelet/models/tiny-gpt2/content/config.json"

assert_ok "create tiny OCI model manifest" \
    R "cat >/tmp/smollm2-135m.yaml <<'EOF'
apiVersion: edgelet.iofog.org/v1
kind: Model
metadata:
  name: smollm2-135m
spec:
  repo: ai/smollm2
  revision: sha256:eb3483481647229668c7625b080c2d6a632bf49535e0714198de5c026d4f41a5
  registry: 1
  files: []
  format: gguf
EOF"

assert_contains "deploy tiny OCI model" "model manifest applied successfully" \
    R "edgelet deploy -f /tmp/smollm2-135m.yaml"

assert_contains "model ls lists smollm2-135m" "smollm2-135m" \
    R "edgelet model ls"

assert_contains "inspect tiny OCI model is Ready" "Ready" \
    R "edgelet model inspect smollm2-135m"

assert_ok "tiny OCI model content materialized on disk" \
    R "set -e
test -d /var/lib/edgelet/models/smollm2-135m/content
find /var/lib/edgelet/models/smollm2-135m/content -type f | grep -q ."

###############################################################################
# Phase 10 — Catalog bind + expanded container fields
###############################################################################
log_step "Phase 10: Catalog bind and expanded container fields"

assert_ok "model ls source column lists local tiny-gpt2" \
    R "set -e
out=\$(edgelet model ls)
echo \"\${out}\" | grep tiny-gpt2 | grep -q local"

assert_ok "create catalog-fields microservice manifest" \
    R "cat >/tmp/catalog-fields.yaml <<'EOF'
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: catalog-fields-test
  labels:
    test: catalog-fields
spec:
  image: docker.io/library/alpine:3.19
  registry: 1
  models:
    bindPath: /models
    permissions: ro
    items:
      - name: tiny-gpt2
  container:
    runAsUser: \"0\"
    runAsGroup: \"65534\"
    readOnlyRootFilesystem: true
    workingDir: /tmp
    entrypoint:
      - /bin/sh
    commands:
      - -lc
      - sleep 14000
    cpuSetCpus: \"0\"
    cpus: 0.5
    memoryLimit: 64
    memoryReservation: 32
    memorySwap: -1
    shmSize: 16
    annotations:
      test.edgelet.io/suite: catalog-fields
    sysctls:
      net.ipv4.tcp_keepalive_time: \"600\"
    ulimits:
      nofile:
        soft: 1024
        hard: 2048
    devices:
      - hostPath: /dev/zero
        containerPath: /dev/edgelet-zero
        permissions: r
    tmpfs:
      - containerPath: /tmp
        size: 16
        mode: \"1777\"
    healthCheck:
      test: [\"CMD\", \"/bin/true\"]
      interval: 30
      timeout: 5
      retries: 1
  schedule: 50
EOF"

assert_contains "deploy catalog-fields microservice" "microservice manifest applied successfully" \
    R "edgelet deploy -f /tmp/catalog-fields.yaml"

assert_ok "catalog-fields microservice reaches running" \
    R "set -e
for i in \$(seq 1 90); do
  inspect=\$(edgelet ms inspect edgelet.catalog-fields-test 2>/dev/null || true)
  if echo \"\${inspect}\" | grep -Eq '^  \"state\": \"running\"' ; then
    cid=\$(echo \"\${inspect}\" | sed -n 's/^  \"containerId\": \"\\([^\"]*\\)\".*/\\1/p' | head -n1 | tr -d '[:space:]')
    if [ -n \"\${cid}\" ] && [ \"\${cid}\" != '-' ]; then
      echo \"\${inspect}\" | sed -n 's/^  \"uuid\": \"\\([^\"]*\\)\".*/\\1/p' | head -n1 | tr -d '[:space:]' >/tmp/catalog-fields.uuid
      echo \"\${cid}\" >/tmp/catalog-fields.cid
      exit 0
    fi
  fi
  sleep 2
done
echo 'catalog-fields-test not running' >&2
edgelet ms inspect edgelet.catalog-fields-test 2>/dev/null || true
exit 1"

assert_contains "ms inspect shows catalog bindPath and item" "\"bindPath\": \"/models\"" \
    R "edgelet ms inspect edgelet.catalog-fields-test"

assert_contains "ms inspect lists tiny-gpt2 catalog item" "tiny-gpt2" \
    R "edgelet ms inspect edgelet.catalog-fields-test"

assert_contains "ms inspect shows catalog permissions" "\"permissions\": \"ro\"" \
    R "edgelet ms inspect edgelet.catalog-fields-test"

assert_ok "container sees catalog files and applied container fields" \
    R "set -e
uuid=\$(cat /tmp/catalog-fields.uuid)
test -n \"\${uuid}\"
edgelet ms exec \"\${uuid}\" -- test -f /models/tiny-gpt2/config.json
pwd=\$(edgelet ms exec \"\${uuid}\" -- pwd)
echo \"\${pwd}\" | grep -qx /tmp
id_out=\$(edgelet ms exec \"\${uuid}\" -- id)
echo \"\${id_out}\" | grep -q 'uid=0'
echo \"\${id_out}\" | grep -q 'gid=65534'
ps_out=\$(edgelet ms exec \"\${uuid}\" -- ps)
echo \"\${ps_out}\" | grep -q sleep
if edgelet ms exec \"\${uuid}\" -- touch /opt/should-fail 2>/dev/null; then
  echo 'read-only root allowed write at /opt' >&2
  exit 1
fi
edgelet ms exec \"\${uuid}\" -- touch /tmp/ok
edgelet ms exec \"\${uuid}\" -- test -c /dev/edgelet-zero
ka=\$(edgelet ms exec \"\${uuid}\" -- cat /proc/sys/net/ipv4/tcp_keepalive_time)
echo \"\${ka}\" | grep -qx 600
limits=\$(edgelet ms exec \"\${uuid}\" -- awk '/Max open files/{print \$4, \$5}' /proc/1/limits)
echo \"\${limits}\" | grep -q '1024 2048'
shm=\$(edgelet ms exec \"\${uuid}\" -- df -k /dev/shm | awk 'NR==2{print \$2}')
test -n \"\${shm}\"
test \"\${shm}\" -ge 14000
test \"\${shm}\" -le 20000
mounts=\$(edgelet ms exec \"\${uuid}\" -- cat /proc/mounts)
echo \"\${mounts}\" | grep -E ' tmpfs /tmp | /tmp tmpfs'
cg_cat() {
  f=\$1
  edgelet ms exec \"\${uuid}\" -- cat /sys/fs/cgroup/\$f 2>/dev/null && return 0
  return 1
}
cpu_max=\$(cg_cat cpu.max)
echo \"\${cpu_max}\" | grep -q '50000'
echo \"\${cpu_max}\" | grep -q '100000'
mem_max=\$(cg_cat memory.max)
echo \"\${mem_max}\" | grep -qx 67108864
cpus=\$(cg_cat cpuset.cpus || cg_cat cpuset.cpus.effective)
echo \"\${cpus}\" | grep -Eq '^0\$|^0-0\$'
mem_low=\$(cg_cat memory.low || true)
if [ -n \"\${mem_low}\" ]; then echo \"\${mem_low}\" | grep -qx 33554432; fi"

assert_ok "model rm tiny-gpt2 refused while microservice is bound" \
    R "set -e
out=\$(edgelet model rm tiny-gpt2 2>&1 || true)
echo \"\${out}\" | grep -q bound"

assert_ok "create catalog-fields two-item manifest" \
    R "awk '/- name: tiny-gpt2/{print; print \"      - name: smollm2-135m\"; next}1' /tmp/catalog-fields.yaml >/tmp/catalog-fields-two.yaml
grep -q smollm2-135m /tmp/catalog-fields-two.yaml"

assert_contains "redeploy catalog with second item" "microservice manifest applied successfully" \
    R "edgelet deploy -f /tmp/catalog-fields-two.yaml"

assert_ok "adding a catalog item does not recreate the container" \
    R "set -e
sleep 3
inspect=\$(edgelet ms inspect edgelet.catalog-fields-test)
cid=\$(echo \"\${inspect}\" | sed -n 's/^  \"containerId\": \"\\([^\"]*\\)\".*/\\1/p' | head -n1 | tr -d '[:space:]')
old=\$(tr -d '[:space:]' </tmp/catalog-fields.cid)
test -n \"\${cid}\"
test \"\${cid}\" = \"\${old}\"
uuid=\$(cat /tmp/catalog-fields.uuid)
for i in \$(seq 1 20); do
  if edgelet ms exec \"\${uuid}\" -- sh -c 'ls /models/smollm2-135m | grep -q .'; then
    exit 0
  fi
  sleep 2
done
echo 'second catalog item not visible in container' >&2
exit 1"

assert_ok "create catalog-fields bindPath-change manifest" \
    R "sed 's|bindPath: /models|bindPath: /weights|' /tmp/catalog-fields.yaml >/tmp/catalog-fields-weights.yaml
grep -q 'bindPath: /weights' /tmp/catalog-fields-weights.yaml"

assert_contains "redeploy catalog with bindPath change" "microservice manifest applied successfully" \
    R "edgelet deploy -f /tmp/catalog-fields-weights.yaml"

assert_ok_verbose "bindPath change recreates container and remounts catalog" \
    R "set -e
old=\$(tr -d '[:space:]' </tmp/catalog-fields.cid)
uuid=\$(tr -d '[:space:]' </tmp/catalog-fields.uuid)
for i in \$(seq 1 60); do
  inspect=\$(edgelet ms inspect edgelet.catalog-fields-test 2>/dev/null || true)
  if echo \"\${inspect}\" | grep -Eq '^  \"state\": \"running\"'; then
    cid=\$(echo \"\${inspect}\" | sed -n 's/^  \"containerId\": \"\\([^\"]*\\)\".*/\\1/p' | head -n1 | tr -d '[:space:]')
    if [ -n \"\${cid}\" ] && [ \"\${cid}\" != \"\${old}\" ] && [ -n \"\${uuid}\" ]; then
      if echo \"\${inspect}\" | grep -q '\"bindPath\": \"/weights\"' &&
         edgelet ms exec \"\${uuid}\" -- test -f /weights/tiny-gpt2/config.json; then
        echo \"\${cid}\" >/tmp/catalog-fields.cid
        exit 0
      fi
    fi
  fi
  sleep 2
done
echo 'bindPath recreate did not produce a new running container' >&2
echo \"old containerId=\${old}\" >&2
edgelet ms inspect edgelet.catalog-fields-test 2>/dev/null || true
exit 1"

assert_ok "create unknown catalog item manifest" \
    R "cat >/tmp/catalog-unknown.yaml <<'EOF'
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: catalog-unknown-test
spec:
  image: docker.io/library/alpine:3.19
  registry: 1
  models:
    bindPath: /models
    permissions: ro
    items:
      - name: missing-model
  container:
    commands:
      - /bin/sh
      - -lc
      - sleep 14000
  schedule: 50
EOF"

assert_ok "local apply rejects unknown catalog model name" \
    R "set -e
out=\$(edgelet deploy -f /tmp/catalog-unknown.yaml 2>&1 || true)
echo \"\${out}\" | grep -q missing-model
echo \"\${out}\" | grep -qi local"

assert_ok "remove catalog-fields microservice" \
    R "set -e
uuid=\$(cat /tmp/catalog-fields.uuid)
edgelet ms rm \"\${uuid}\""

assert_ok "catalog-fields microservice is gone from ms ls after rm" \
    R "set -e
out=\$(edgelet ms ls)
! echo \"\${out}\" | grep -q catalog-fields-test"

assert_ok "remove tiny Hugging Face model after unbind" \
    R "edgelet model rm tiny-gpt2"

assert_ok "remove tiny OCI model after unbind" \
    R "edgelet model rm smollm2-135m"

assert_ok "prune models after remove" \
    R "edgelet model prune"

###############################################################################
# Phase 11 — Persistent VOLUME scope, retain, explicit reclaim
###############################################################################
log_step "Phase 11: Persistent VOLUME retain and reclaim"

assert_ok "install persistent volume helpers" \
    R "cat >/tmp/vol-it-ops.sh <<'EOF'
wait_ms_running() {
  local name=\"\$1\"
  local out_file=\"\$2\"
  local i inspect uuid
  for i in \$(seq 1 90); do
    inspect=\$(edgelet ms inspect \"edgelet.\${name}\" 2>/dev/null || true)
    if echo \"\${inspect}\" | grep -Eq '^  \"state\": \"running\"'; then
      uuid=\$(echo \"\${inspect}\" | sed -n 's/^  \"uuid\": \"\\([^\"]*\\)\".*/\\1/p' | head -n1 | tr -d '[:space:]')
      if [ -n \"\${uuid}\" ]; then
        echo \"\${uuid}\" >\"\${out_file}\"
        return 0
      fi
    fi
    sleep 2
  done
  echo \"\${name} not running\" >&2
  edgelet ms inspect \"edgelet.\${name}\" 2>/dev/null || true
  return 1
}

wait_units_ready() {
  local i
  for i in \$(seq 1 90); do
    if systemctl is-active --quiet edgelet \\
       && systemctl is-active --quiet edgelet-containerd \\
       && test -S /run/edgelet/edgelet.sock \\
       && test -S /run/edgelet/containerd.sock \\
       && edgelet system status >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  echo 'edgelet units not ready' >&2
  systemctl status edgelet edgelet-containerd --no-pager || true
  systemctl show edgelet-containerd -p Result -p NRestarts -p ActiveState || true
  journalctl -u edgelet-containerd -n 40 --no-pager || true
  return 1
}

require_data_plane() {
  if systemctl is-active --quiet edgelet-containerd \\
     && test -S /run/edgelet/containerd.sock; then
    return 0
  fi
  echo 'data plane is not active' >&2
  systemctl status edgelet-containerd --no-pager || true
  ls -l /run/edgelet/containerd.sock >&2 || true
  return 1
}

restart_data_plane() {
  systemctl reset-failed edgelet-containerd 2>/dev/null || true
  if ! systemctl restart edgelet-containerd; then
    echo 'edgelet-containerd restart failed; reset-failed and start' >&2
    systemctl reset-failed edgelet-containerd 2>/dev/null || true
    systemctl start edgelet-containerd || true
  fi
  wait_units_ready
}

dump_volume_markers() {
  local uuid=\"\$1\"
  echo \"uuid=\${uuid}\" >&2
  echo '--- volume ls ---' >&2
  edgelet volume ls >&2 || true
  echo '--- private ---' >&2
  ls -la /var/lib/edgelet/volumes/data/\${uuid}/it-mydata/ >&2 || true
  echo '--- shared ---' >&2
  ls -la /var/lib/edgelet/volumes/shared/it-shared-config/ >&2 || true
  echo '--- bind ---' >&2
  ls -la /tmp/edgelet-it-bind/ >&2 || true
}

assert_host_markers() {
  local uuid=\"\$1\"
  local need_bind=\"\$2\"
  if [ -z \"\${uuid}\" ]; then
    echo 'volume-it uuid is empty' >&2
    dump_volume_markers \"\${uuid}\"
    return 1
  fi
  if ! test -f /var/lib/edgelet/volumes/data/\${uuid}/it-mydata/marker; then
    echo \"missing private marker for \${uuid}\" >&2
    dump_volume_markers \"\${uuid}\"
    return 1
  fi
  if ! test -f /var/lib/edgelet/volumes/shared/it-shared-config/marker; then
    echo 'missing shared marker' >&2
    dump_volume_markers \"\${uuid}\"
    return 1
  fi
  if [ \"\${need_bind}\" = bind ] && ! test -f /tmp/edgelet-it-bind/host-marker; then
    echo 'missing BIND host-marker' >&2
    dump_volume_markers \"\${uuid}\"
    return 1
  fi
}

write_host_markers() {
  local uuid=\"\$1\"
  if [ -z \"\${uuid}\" ]; then
    echo 'volume-it uuid is empty' >&2
    return 1
  fi
  mkdir -p /var/lib/edgelet/volumes/data/\${uuid}/it-mydata /var/lib/edgelet/volumes/shared/it-shared-config
  echo keep-private >/var/lib/edgelet/volumes/data/\${uuid}/it-mydata/marker
  echo keep-shared >/var/lib/edgelet/volumes/shared/it-shared-config/marker
}

wait_container_sees_markers() {
  local a_uuid=\"\$1\"
  local b_uuid=\"\$2\"
  local i
  for i in \$(seq 1 90); do
    if edgelet ms exec \"\${a_uuid}\" -- test -f /data/marker \\
       && edgelet ms exec \"\${a_uuid}\" -- test -f /shared/marker \\
       && edgelet ms exec \"\${a_uuid}\" -- test -f /host-data/host-marker \\
       && edgelet ms exec \"\${b_uuid}\" -- test -f /shared/marker; then
      return 0
    fi
    sleep 2
  done
  echo 'containers did not see host VOLUME markers' >&2
  dump_volume_markers \"\${a_uuid}\"
  return 1
}

rm_ms_by_name() {
  local name=\"\$1\"
  local uuid
  uuid=\$(edgelet ms ls 2>/dev/null | awk -v n=\"\${name}\" '\$3==n{print \$1; exit}')
  if [ -n \"\${uuid}\" ]; then
    edgelet ms rm \"\${uuid}\" >/dev/null 2>&1 || true
  fi
}

rm_leftover_workloads() {
  local uuid name
  edgelet ms ls 2>/dev/null | awk 'NR>1 {print \$1, \$3}' | while read -r uuid name; do
    case \"\${name}\" in
      vol-it-a|vol-it-b|'') continue ;;
    esac
    if [ -n \"\${uuid}\" ]; then
      edgelet ms rm \"\${uuid}\" >/dev/null 2>&1 || true
    fi
  done
}
EOF
chmod +x /tmp/vol-it-ops.sh"

assert_ok "remove leftover volume-it microservices from a prior run" \
    R "set -e
source /tmp/vol-it-ops.sh
rm_leftover_workloads
rm_ms_by_name vol-it-a
rm_ms_by_name vol-it-b
edgelet volume rm --shared it-shared-config --force >/dev/null 2>&1 || true
edgelet volume prune --orphans --yes --force >/dev/null 2>&1 || true
rm -rf /tmp/edgelet-it-bind
mkdir -p /tmp/edgelet-it-bind
echo bind-host >/tmp/edgelet-it-bind/host-marker"

assert_ok "create private+shared+bind volume-it manifest" \
    R "cat >/tmp/vol-it-a.yaml <<'EOF'
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: vol-it-a
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
      - hostDestination: it-mydata
        containerDestination: /data
        accessMode: rw
        type: volume
        scope: foo
      - hostDestination: it-shared-config
        containerDestination: /shared
        accessMode: rw
        type: volume
        scope: shared
      - hostDestination: /tmp/edgelet-it-bind
        containerDestination: /host-data
        accessMode: rw
        type: bind
        scope: shared
  schedule: 50
EOF"

assert_ok "create second shared-volume consumer manifest" \
    R "cat >/tmp/vol-it-b.yaml <<'EOF'
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: vol-it-b
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
      - hostDestination: it-shared-config
        containerDestination: /shared
        accessMode: rw
        type: volume
        scope: shared
  schedule: 50
EOF"

assert_contains "deploy volume-it private/shared/bind microservice" "microservice manifest applied successfully" \
    R "edgelet deploy -f /tmp/vol-it-a.yaml"

assert_contains "deploy volume-it second shared consumer" "microservice manifest applied successfully" \
    R "edgelet deploy -f /tmp/vol-it-b.yaml"

assert_ok_verbose "volume-it microservices reach running" \
    R "set -e
source /tmp/vol-it-ops.sh
wait_ms_running vol-it-a /tmp/vol-it-a.uuid
wait_ms_running vol-it-b /tmp/vol-it-b.uuid"

assert_ok_verbose "unknown VOLUME scope is stored private and BIND scope is ignored" \
    R "set -e
a=\$(cat /tmp/vol-it-a.uuid)
b=\$(cat /tmp/vol-it-b.uuid)
test -n \"\${a}\"
test -n \"\${b}\"
ls_json=\$(edgelet -o json volume ls)
echo \"\${ls_json}\" | grep -q '\"name\": \"it-mydata\"'
echo \"\${ls_json}\" | grep -q '\"scope\": \"private\"'
echo \"\${ls_json}\" | grep -q '\"name\": \"it-shared-config\"'
echo \"\${ls_json}\" | grep -q '\"scope\": \"shared\"'
echo \"\${ls_json}\" | grep -q \"\${a}\"
echo \"\${ls_json}\" | grep -q \"\${b}\"
! echo \"\${ls_json}\" | grep -q '/tmp/edgelet-it-bind'
test -d /var/lib/edgelet/volumes/data/\${a}/it-mydata
test -d /var/lib/edgelet/volumes/shared/it-shared-config
! test -d /var/lib/edgelet/volumes/shared/it-mydata"

assert_ok_verbose "write markers into private, shared, and BIND mounts" \
    R "set -e
source /tmp/vol-it-ops.sh
a=\$(cat /tmp/vol-it-a.uuid)
b=\$(cat /tmp/vol-it-b.uuid)
write_host_markers \"\${a}\"
wait_container_sees_markers \"\${a}\" \"\${b}\"
assert_host_markers \"\${a}\" bind"

assert_ok "enable pruningFrequency for restart retain check" \
    R "set -e
orig=\$(edgelet -o json system info | awk -F': ' '/pruningFrequency/{print \$2}' | tr -d ' ,')
echo \"\${orig}\" >/tmp/vol-it-orig-pf
edgelet config --pruning-frequency 1 >/dev/null"

assert_ok_verbose "markers remain after enabling pruningFrequency" \
    R "set -e
source /tmp/vol-it-ops.sh
a=\$(cat /tmp/vol-it-a.uuid)
assert_host_markers \"\${a}\""

assert_ok_verbose "control and data-plane restart leaves VOLUME markers" \
    R "set -e
source /tmp/vol-it-ops.sh
restart_data_plane
systemctl restart edgelet
wait_units_ready
wait_ms_running vol-it-a /tmp/vol-it-a.uuid
wait_ms_running vol-it-b /tmp/vol-it-b.uuid
a=\$(cat /tmp/vol-it-a.uuid)
b=\$(cat /tmp/vol-it-b.uuid)
assert_host_markers \"\${a}\"
wait_container_sees_markers \"\${a}\" \"\${b}\""

assert_ok_verbose "system prune volumes refuses and points at volume prune" \
    R "set -e
source /tmp/vol-it-ops.sh
out=\$(edgelet system prune volumes 2>&1 || true)
echo \"\${out}\"
echo \"\${out}\" | grep -q 'edgelet volume prune'
a=\$(cat /tmp/vol-it-a.uuid)
assert_host_markers \"\${a}\""

assert_ok_verbose "system prune all does not delete persistent VOLUME data" \
    R "set -e
source /tmp/vol-it-ops.sh
edgelet system prune all
a=\$(cat /tmp/vol-it-a.uuid)
assert_host_markers \"\${a}\" bind"

assert_ok_verbose "volume prune default is dry-run" \
    R "set -e
source /tmp/vol-it-ops.sh
out=\$(edgelet -o json volume prune)
echo \"\${out}\" | grep -q '\"dryRun\": true'
a=\$(cat /tmp/vol-it-a.uuid)
assert_host_markers \"\${a}\""

assert_ok_verbose "volume rm --force while mounted is refused" \
    R "set -e
source /tmp/vol-it-ops.sh
require_data_plane
a=\$(cat /tmp/vol-it-a.uuid)
out=\$(edgelet volume rm \"\${a}\" it-mydata --force 2>&1 || true)
echo \"\${out}\"
echo \"\${out}\" | grep -qiE 'mounted|keep-set|CONFLICT'
assert_host_markers \"\${a}\""

assert_ok_verbose "shared volume rm --force while a consumer is running is refused" \
    R "set -e
source /tmp/vol-it-ops.sh
require_data_plane
out=\$(edgelet volume rm --shared it-shared-config --force 2>&1 || true)
echo \"\${out}\"
echo \"\${out}\" | grep -qiE 'mounted|keep-set|consumers|CONFLICT'
a=\$(cat /tmp/vol-it-a.uuid)
assert_host_markers \"\${a}\""

assert_ok_verbose "ms rm retains private VOLUME and shared disk" \
    R "set -e
source /tmp/vol-it-ops.sh
require_data_plane
a=\$(cat /tmp/vol-it-a.uuid)
echo \"\${a}\" >/tmp/vol-it-a.uuid.removed
edgelet ms rm \"\${a}\"
assert_host_markers \"\${a}\"
wait_ms_running vol-it-b /tmp/vol-it-b.uuid
b=\$(cat /tmp/vol-it-b.uuid)
edgelet ms exec \"\${b}\" -- test -f /shared/marker
ls_json=\$(edgelet -o json volume ls)
echo \"\${ls_json}\" | grep -q 'it-shared-config'
echo \"\${ls_json}\" | grep -q \"\${b}\""

assert_contains "redeploy vol-it-a remounts shared VOLUME data" "microservice manifest applied successfully" \
    R "set -e
source /tmp/vol-it-ops.sh
require_data_plane
edgelet deploy -f /tmp/vol-it-a.yaml"

assert_ok_verbose "new private UUID is empty; shared marker remounts" \
    R "set -e
source /tmp/vol-it-ops.sh
require_data_plane
wait_ms_running vol-it-a /tmp/vol-it-a.uuid
a=\$(cat /tmp/vol-it-a.uuid)
old=\$(cat /tmp/vol-it-a.uuid.removed)
test \"\${a}\" != \"\${old}\"
if ! test -f /var/lib/edgelet/volumes/data/\${old}/it-mydata/marker; then
  echo \"old private marker missing for \${old}\" >&2
  dump_volume_markers \"\${old}\"
  exit 1
fi
if ! test -f /var/lib/edgelet/volumes/shared/it-shared-config/marker; then
  echo 'shared marker missing after redeploy' >&2
  dump_volume_markers \"\${a}\"
  exit 1
fi
ok=0
for i in \$(seq 1 90); do
  if edgelet ms exec \"\${a}\" -- test -f /shared/marker; then
    ok=1
    break
  fi
  sleep 2
done
if [ \"\${ok}\" -ne 1 ]; then
  echo 'new vol-it-a did not remount shared marker' >&2
  dump_volume_markers \"\${a}\"
  exit 1
fi
if edgelet ms exec \"\${a}\" -- test -f /data/marker; then
  echo 'new private volume unexpectedly reused previous UUID data' >&2
  exit 1
fi"

assert_ok "remove volume-it microservices" \
    R "set -e
source /tmp/vol-it-ops.sh
rm_ms_by_name vol-it-a
rm_ms_by_name vol-it-b"

assert_ok_verbose "explicit reclaim destroys private and shared VOLUME data" \
    R "set -e
old=\$(cat /tmp/vol-it-a.uuid.removed)
a=\$(cat /tmp/vol-it-a.uuid 2>/dev/null || true)
edgelet volume rm \"\${old}\" it-mydata --force >/dev/null
if [ -n \"\${a}\" ] && [ \"\${a}\" != \"\${old}\" ]; then
  edgelet volume rm \"\${a}\" it-mydata --force >/dev/null
fi
edgelet volume rm --shared it-shared-config --force >/dev/null
! test -e /var/lib/edgelet/volumes/data/\${old}/it-mydata/marker
! test -e /var/lib/edgelet/volumes/shared/it-shared-config/marker
test -f /tmp/edgelet-it-bind/host-marker
ls_json=\$(edgelet -o json volume ls)
! echo \"\${ls_json}\" | grep -q 'it-shared-config'
! echo \"\${ls_json}\" | grep -q 'it-mydata'"

assert_ok "restore pruningFrequency and BIND host dir" \
    R "set -e
orig=\$(cat /tmp/vol-it-orig-pf 2>/dev/null || true)
if [ -n \"\${orig}\" ]; then
  edgelet config --pruning-frequency \"\${orig}\" >/dev/null || true
fi
rm -rf /tmp/edgelet-it-bind /tmp/vol-it-a.yaml /tmp/vol-it-b.yaml /tmp/vol-it-ops.sh /tmp/vol-it-a.uuid /tmp/vol-it-b.uuid /tmp/vol-it-a.uuid.removed /tmp/vol-it-orig-pf"

###############################################################################
# Phase 12 — Tiny HF Knowledge pull + catalog bind
###############################################################################
log_step "Phase 12: Tiny Hugging Face Knowledge pull and catalog bind"

assert_ok "create tiny Hugging Face knowledge manifest" \
    R "cat >/tmp/tiny-docs.yaml <<'EOF'
apiVersion: edgelet.iofog.org/v1
kind: Knowledge
metadata:
  name: tiny-docs
spec:
  repo: hf-internal-testing/dataset_with_data_files
  revision: c19550d35263090b1ec2bfefdbd737431fafec40
  registry: 3
  files:
    - data/train.txt
  format: unknown
EOF"

assert_contains "deploy tiny Hugging Face knowledge" "knowledge manifest applied successfully" \
    R "edgelet deploy -f /tmp/tiny-docs.yaml"

assert_contains "knowledge ls lists tiny-docs" "tiny-docs" \
    R "edgelet knowledge ls"

assert_contains "inspect tiny knowledge is Ready" "Ready" \
    R "edgelet knowledge inspect tiny-docs"

assert_ok "tiny Hugging Face knowledge content materialized on disk" \
    R "test -f /var/lib/edgelet/knowledge/tiny-docs/content/data/train.txt"

assert_ok "knowledge pull does not write the models store" \
    R "set -e
test -f /var/lib/edgelet/knowledge/tiny-docs/content/data/train.txt
! test -e /var/lib/edgelet/models/tiny-docs
! test -e /var/lib/edgelet/models/oci-store/tiny-docs"

assert_ok "create second tiny Hugging Face knowledge manifest" \
    R "cat >/tmp/tiny-corpus.yaml <<'EOF'
apiVersion: edgelet.iofog.org/v1
kind: Knowledge
metadata:
  name: tiny-corpus
spec:
  repo: hf-internal-testing/dataset_with_data_files
  revision: c19550d35263090b1ec2bfefdbd737431fafec40
  registry: 3
  files:
    - data/test.txt
  format: unknown
EOF"

assert_contains "deploy second tiny Hugging Face knowledge" "knowledge manifest applied successfully" \
    R "edgelet deploy -f /tmp/tiny-corpus.yaml"

assert_ok "second knowledge content materialized on disk" \
    R "test -f /var/lib/edgelet/knowledge/tiny-corpus/content/data/test.txt"

assert_ok "knowledge ls source column lists local tiny-docs" \
    R "set -e
out=\$(edgelet knowledge ls)
echo \"\${out}\" | grep tiny-docs | grep -q local"

assert_ok "create knowledge-catalog microservice manifest" \
    R "cat >/tmp/knowledge-catalog.yaml <<'EOF'
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: knowledge-catalog-test
  labels:
    test: knowledge-catalog
spec:
  image: docker.io/library/alpine:3.19
  registry: 1
  knowledge:
    bindPath: /knowledge
    permissions: ro
    items:
      - name: tiny-docs
  container:
    commands:
      - /bin/sh
      - -lc
      - sleep 14000
  schedule: 50
EOF"

assert_contains "deploy knowledge-catalog microservice" "microservice manifest applied successfully" \
    R "edgelet deploy -f /tmp/knowledge-catalog.yaml"

assert_ok "knowledge-catalog microservice reaches running" \
    R "set -e
for i in \$(seq 1 90); do
  inspect=\$(edgelet ms inspect edgelet.knowledge-catalog-test 2>/dev/null || true)
  if echo \"\${inspect}\" | grep -Eq '^  \"state\": \"running\"' ; then
    cid=\$(echo \"\${inspect}\" | sed -n 's/^  \"containerId\": \"\\([^\"]*\\)\".*/\\1/p' | head -n1 | tr -d '[:space:]')
    if [ -n \"\${cid}\" ] && [ \"\${cid}\" != '-' ]; then
      echo \"\${inspect}\" | sed -n 's/^  \"uuid\": \"\\([^\"]*\\)\".*/\\1/p' | head -n1 | tr -d '[:space:]' >/tmp/knowledge-catalog.uuid
      echo \"\${cid}\" >/tmp/knowledge-catalog.cid
      exit 0
    fi
  fi
  sleep 2
done
echo 'knowledge-catalog-test not running' >&2
edgelet ms inspect edgelet.knowledge-catalog-test 2>/dev/null || true
exit 1"

assert_contains "ms inspect shows knowledge bindPath and item" "\"bindPath\": \"/knowledge\"" \
    R "edgelet ms inspect edgelet.knowledge-catalog-test"

assert_contains "ms inspect lists tiny-docs knowledge item" "tiny-docs" \
    R "edgelet ms inspect edgelet.knowledge-catalog-test"

assert_contains "ms inspect shows knowledge permissions" "\"permissions\": \"ro\"" \
    R "edgelet ms inspect edgelet.knowledge-catalog-test"

assert_ok "container sees knowledge catalog files" \
    R "set -e
uuid=\$(cat /tmp/knowledge-catalog.uuid)
test -n \"\${uuid}\"
edgelet ms exec \"\${uuid}\" -- test -f /knowledge/tiny-docs/data/train.txt"

assert_ok "knowledge rm tiny-docs refused while microservice is bound" \
    R "set -e
out=\$(edgelet knowledge rm tiny-docs 2>&1 || true)
echo \"\${out}\" | grep -q bound"

assert_ok "create knowledge-catalog two-item manifest" \
    R "awk '/- name: tiny-docs/{print; print \"      - name: tiny-corpus\"; next}1' /tmp/knowledge-catalog.yaml >/tmp/knowledge-catalog-two.yaml
grep -q tiny-corpus /tmp/knowledge-catalog-two.yaml"

assert_contains "redeploy knowledge catalog with second item" "microservice manifest applied successfully" \
    R "edgelet deploy -f /tmp/knowledge-catalog-two.yaml"

assert_ok "adding a knowledge item does not recreate the container" \
    R "set -e
sleep 3
inspect=\$(edgelet ms inspect edgelet.knowledge-catalog-test)
cid=\$(echo \"\${inspect}\" | sed -n 's/^  \"containerId\": \"\\([^\"]*\\)\".*/\\1/p' | head -n1 | tr -d '[:space:]')
old=\$(tr -d '[:space:]' </tmp/knowledge-catalog.cid)
test -n \"\${cid}\"
test \"\${cid}\" = \"\${old}\"
uuid=\$(cat /tmp/knowledge-catalog.uuid)
for i in \$(seq 1 20); do
  if edgelet ms exec \"\${uuid}\" -- test -f /knowledge/tiny-corpus/data/test.txt; then
    exit 0
  fi
  sleep 2
done
echo 'second knowledge item not visible in container' >&2
exit 1"

assert_ok "create knowledge-catalog bindPath-change manifest" \
    R "sed 's|bindPath: /knowledge|bindPath: /corpus|' /tmp/knowledge-catalog.yaml >/tmp/knowledge-catalog-corpus.yaml
grep -q 'bindPath: /corpus' /tmp/knowledge-catalog-corpus.yaml"

assert_contains "redeploy knowledge catalog with bindPath change" "microservice manifest applied successfully" \
    R "edgelet deploy -f /tmp/knowledge-catalog-corpus.yaml"

assert_ok_verbose "knowledge bindPath change recreates container and remounts catalog" \
    R "set -e
old=\$(tr -d '[:space:]' </tmp/knowledge-catalog.cid)
uuid=\$(tr -d '[:space:]' </tmp/knowledge-catalog.uuid)
for i in \$(seq 1 60); do
  inspect=\$(edgelet ms inspect edgelet.knowledge-catalog-test 2>/dev/null || true)
  if echo \"\${inspect}\" | grep -Eq '^  \"state\": \"running\"'; then
    cid=\$(echo \"\${inspect}\" | sed -n 's/^  \"containerId\": \"\\([^\"]*\\)\".*/\\1/p' | head -n1 | tr -d '[:space:]')
    if [ -n \"\${cid}\" ] && [ \"\${cid}\" != \"\${old}\" ] && [ -n \"\${uuid}\" ]; then
      if echo \"\${inspect}\" | grep -q '\"bindPath\": \"/corpus\"' &&
         edgelet ms exec \"\${uuid}\" -- test -f /corpus/tiny-docs/data/train.txt; then
        echo \"\${cid}\" >/tmp/knowledge-catalog.cid
        exit 0
      fi
    fi
  fi
  sleep 2
done
echo 'knowledge bindPath recreate did not produce a new running container' >&2
echo \"old containerId=\${old}\" >&2
edgelet ms inspect edgelet.knowledge-catalog-test 2>/dev/null || true
exit 1"

assert_ok "create unknown knowledge catalog item manifest" \
    R "cat >/tmp/knowledge-unknown.yaml <<'EOF'
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: knowledge-unknown-test
spec:
  image: docker.io/library/alpine:3.19
  registry: 1
  knowledge:
    bindPath: /knowledge
    permissions: ro
    items:
      - name: missing-knowledge
  container:
    commands:
      - /bin/sh
      - -lc
      - sleep 14000
  schedule: 50
EOF"

assert_ok "local apply rejects unknown catalog knowledge name" \
    R "set -e
out=\$(edgelet deploy -f /tmp/knowledge-unknown.yaml 2>&1 || true)
echo \"\${out}\" | grep -q missing-knowledge
echo \"\${out}\" | grep -qi local"

assert_ok "remove knowledge-catalog microservice" \
    R "set -e
uuid=\$(cat /tmp/knowledge-catalog.uuid)
edgelet ms rm \"\${uuid}\""

assert_ok "knowledge-catalog microservice is gone from ms ls after rm" \
    R "set -e
out=\$(edgelet ms ls)
! echo \"\${out}\" | grep -q knowledge-catalog-test"

assert_ok "create tiny Hugging Face model for dual catalog" \
    R "cat >/tmp/tiny-gpt2-knowledge.yaml <<'EOF'
apiVersion: edgelet.iofog.org/v1
kind: Model
metadata:
  name: tiny-gpt2
spec:
  repo: hf-internal-testing/tiny-random-gpt2
  revision: 71034c5d8bde858ff824298bdedc65515b97d2b9
  registry: 3
  files:
    - config.json
  format: unknown
EOF"

assert_contains "deploy tiny Hugging Face model for dual catalog" "model manifest applied successfully" \
    R "edgelet deploy -f /tmp/tiny-gpt2-knowledge.yaml"

assert_ok "create dual-catalog microservice manifest" \
    R "cat >/tmp/knowledge-dual-catalog.yaml <<'EOF'
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: knowledge-dual-catalog-test
  labels:
    test: knowledge-dual-catalog
spec:
  image: docker.io/library/alpine:3.19
  registry: 1
  models:
    bindPath: /models
    permissions: ro
    items:
      - name: tiny-gpt2
  knowledge:
    bindPath: /knowledge
    permissions: ro
    items:
      - name: tiny-docs
  container:
    commands:
      - /bin/sh
      - -lc
      - sleep 14000
  schedule: 50
EOF"

assert_contains "deploy dual-catalog microservice" "microservice manifest applied successfully" \
    R "edgelet deploy -f /tmp/knowledge-dual-catalog.yaml"

assert_ok "dual-catalog microservice reaches running" \
    R "set -e
for i in \$(seq 1 90); do
  inspect=\$(edgelet ms inspect edgelet.knowledge-dual-catalog-test 2>/dev/null || true)
  if echo \"\${inspect}\" | grep -Eq '^  \"state\": \"running\"' ; then
    cid=\$(echo \"\${inspect}\" | sed -n 's/^  \"containerId\": \"\\([^\"]*\\)\".*/\\1/p' | head -n1 | tr -d '[:space:]')
    if [ -n \"\${cid}\" ] && [ \"\${cid}\" != '-' ]; then
      echo \"\${inspect}\" | sed -n 's/^  \"uuid\": \"\\([^\"]*\\)\".*/\\1/p' | head -n1 | tr -d '[:space:]' >/tmp/knowledge-dual.uuid
      exit 0
    fi
  fi
  sleep 2
done
echo 'knowledge-dual-catalog-test not running' >&2
edgelet ms inspect edgelet.knowledge-dual-catalog-test 2>/dev/null || true
exit 1"

assert_ok "container sees both model and knowledge catalog files" \
    R "set -e
uuid=\$(cat /tmp/knowledge-dual.uuid)
test -n \"\${uuid}\"
edgelet ms exec \"\${uuid}\" -- test -f /models/tiny-gpt2/config.json
edgelet ms exec \"\${uuid}\" -- test -f /knowledge/tiny-docs/data/train.txt"

assert_ok "remove dual-catalog microservice" \
    R "set -e
uuid=\$(cat /tmp/knowledge-dual.uuid)
edgelet ms rm \"\${uuid}\""

assert_ok "system prune all does not delete Knowledge trees" \
    R "set -e
edgelet system prune all
test -f /var/lib/edgelet/knowledge/tiny-docs/content/data/train.txt
test -f /var/lib/edgelet/knowledge/tiny-corpus/content/data/test.txt"

assert_ok "remove tiny Hugging Face knowledge after unbind" \
    R "edgelet knowledge rm tiny-docs"

assert_ok "remove second tiny Hugging Face knowledge after unbind" \
    R "edgelet knowledge rm tiny-corpus"

assert_ok "prune knowledge after remove" \
    R "edgelet knowledge prune"

assert_ok "knowledge trees are gone after prune" \
    R "set -e
! test -e /var/lib/edgelet/knowledge/tiny-docs
! test -e /var/lib/edgelet/knowledge/tiny-corpus"

assert_ok "remove tiny Hugging Face model after dual catalog" \
    R "edgelet model rm tiny-gpt2"

assert_ok "prune models after dual catalog" \
    R "edgelet model prune"

###############################################################################
# Summary
###############################################################################
print_summary
