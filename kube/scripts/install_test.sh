#!/bin/sh
# Check the install script and the kernel setup program.
set -eu

cd "$(dirname "$0")/.."

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

need() {
	if ! grep -q "$1" "$2"; then
		fail "missing [$1] in $2"
	fi
}

mode_of() {
	if [ "$(uname -s)" = "Darwin" ]; then
		stat -f '%Lp' "$1"
	else
		stat -c '%a' "$1"
	fi
}

if grep -n 'edgelet\.service' \
	scripts/install.sh \
	packaging/systemd/edgelet-kube.service \
	packaging/systemd/prepare-kernel.sh
then
	fail "agent unit name appears in the install files"
fi

if grep -n 'rm ' scripts/install.sh; then
	fail "install script removes files"
fi
if grep -n -E 'ota|drain' scripts/install.sh packaging/systemd/edgelet-kube.service; then
	fail "agent upgrade path is referenced"
fi

unit=packaging/systemd/edgelet-kube.service
need '^ExecStart=/usr/local/bin/edgelet-kube server$' "$unit"
need '^KillMode=process$' "$unit"
need '^Delegate=yes$' "$unit"
need '^Restart=always$' "$unit"
need '^RestartSec=5s$' "$unit"
need '^ExecStartPre=/usr/libexec/edgelet-kube/prepare-kernel$' "$unit"

if grep -q 'DelegateSubgroup' "$unit"; then
	fail "unit delegates a subgroup"
fi

helper=packaging/systemd/prepare-kernel.sh
need 'modprobe br_netfilter' "$helper"
need 'sysctl -w net.ipv4.ip_forward=1' "$helper"
need 'sysctl -w net.bridge.bridge-nf-call-iptables=1' "$helper"
need '/etc/edgelet-kube/config.yaml' "$helper"

work=$(mktemp -d)
cleanup() {
	if [ -f "$work/pids" ]; then
		while read -r pid; do
			kill "$pid" 2>/dev/null || true
		done <"$work/pids"
	fi
	rm -rf "$work"
}
trap cleanup EXIT
unset EDGELET_KUBE_PROTECT_KERNEL_DEFAULTS || true
unset EDGELET_KUBE_CONFIG_FILE || true

config_real=/etc/edgelet-kube/config.yaml
if [ -e "$config_real" ]; then
	cksum "$config_real" >"$work/config-before"
fi

mkdir -p "$work/units"
printf '%s\n' '[Unit]' 'Description=agent' >"$work/units/edgelet.service"
cksum "$work/units/edgelet.service" >"$work/agent-before"

# The installed file stands in for the thin binary and execs the fat runtime
# with the same argv, so the invoked path stays argv0.
command -v cc >/dev/null 2>&1 || fail "cc is required"
cat >"$work/thin.c" <<'EOF'
#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>

int main(int argc, char **argv) {
	char *fat = getenv("EDGELET_KUBE_FAT");
	if (fat == NULL || fat[0] == '\0') {
		fprintf(stderr, "missing fat runtime\n");
		return 1;
	}
	execv(fat, argv);
	perror("exec fat runtime");
	return 127;
}
EOF
cat >"$work/fat.c" <<'EOF'
#include <stdio.h>

int main(int argc, char **argv) {
	int i;
	printf("fat-elf\n");
	for (i = 0; i < argc; i++) {
		printf("%s\n", argv[i]);
	}
	return 0;
}
EOF
cc -o "$work/thin" "$work/thin.c"
cc -o "$work/fat" "$work/fat.c"
chmod 0755 "$work/thin" "$work/fat"

cat >"$work/systemctl" <<EOF
#!/bin/sh
printf '%s\n' "\$*" >> "$work/systemctl.log"
EOF
chmod 0755 "$work/systemctl"

EDGELET_KUBE_BIN_DIR="$work/bin" \
EDGELET_KUBE_UNIT_DIR="$work/units" \
EDGELET_KUBE_LIBEXEC_DIR="$work/libexec" \
EDGELET_KUBE_SYSTEMCTL="$work/systemctl" \
	sh scripts/install.sh --bin-path="$work/thin"

cmp -s "$work/thin" "$work/bin/edgelet-kube" || fail "installed binary differs"
[ ! -L "$work/bin/edgelet-kube" ] || fail "installed binary is a symlink"
for name in kubectl ctr crictl; do
	link=$work/bin/$name
	[ -L "$link" ] || fail "$name is not a symlink"
	[ "$(readlink "$link")" = "$work/bin/edgelet-kube" ] || fail "$name does not point at the thin binary"
done
grep -a -q 'fat-elf' "$work/fat" || fail "fat runtime missing marker"
if grep -a -q 'fat-elf' "$work/bin/edgelet-kube"; then
	fail "thin binary contains the fat marker"
fi
run_client() {
	name=$1
	shift
	EDGELET_KUBE_FAT="$work/fat" \
		"$work/bin/$name" "$@" >"$work/client.out" || fail "$name failed"
	printf '%s\n' fat-elf "$work/bin/$name" "$@" >"$work/client.want"
	cmp -s "$work/client.want" "$work/client.out" || {
		echo "$name output:" >&2
		cat "$work/client.out" >&2
		fail "$name did not reach the fat runtime"
	}
}
run_client kubectl version
run_client ctr version
cmp -s "$unit" "$work/units/edgelet-kube.service" || fail "installed unit differs"
cmp -s "$helper" "$work/libexec/prepare-kernel" || fail "installed kernel program differs"
[ "$(mode_of "$work/bin/edgelet-kube")" = "755" ] || fail "binary mode"
[ "$(mode_of "$work/libexec/prepare-kernel")" = "755" ] || fail "kernel program mode"
[ "$(mode_of "$work/units/edgelet-kube.service")" = "644" ] || fail "unit mode"

cksum "$work/units/edgelet.service" >"$work/agent-after"
cmp -s "$work/agent-before" "$work/agent-after" || fail "agent unit was changed"

printf '%s\n' \
	'daemon-reload' \
	'enable edgelet-kube.service' \
	'start edgelet-kube.service' >"$work/systemctl.want"
cmp -s "$work/systemctl.want" "$work/systemctl.log" || {
	echo "systemctl log:" >&2
	cat "$work/systemctl.log" >&2
	fail "systemctl calls"
}

if [ -e "$config_real" ]; then
	cksum "$config_real" >"$work/config-after"
	cmp -s "$work/config-before" "$work/config-after" || fail "config file changed"
else
	[ ! -e "$config_real" ] || fail "config file was created"
fi
[ ! -e "$work/etc/edgelet-kube/config.yaml" ] || fail "sandbox config was created"

help_text=$(sh scripts/install.sh --help)
if printf '%s\n' "$help_text" | grep -q 'edgelet\.service'; then
	fail "help mentions the agent unit"
fi

if sh scripts/install.sh --bin-path= >/dev/null 2>&1; then
	fail "empty --bin-path succeeded"
fi
if sh scripts/install.sh --bin-path="$work/missing-binary" >/dev/null 2>&1; then
	fail "missing binary succeeded"
fi

stubs=$work/stubs
mkdir -p "$stubs"
cat >"$stubs/modprobe" <<EOF
#!/bin/sh
printf '%s\n' "modprobe \$*" >> "\$LOG"
EOF
cat >"$stubs/sysctl" <<EOF
#!/bin/sh
printf '%s\n' "sysctl \$*" >> "\$LOG"
EOF
chmod 0755 "$stubs/modprobe" "$stubs/sysctl"

# ExecStartPre runs to completion, then the server process starts.
run_pre_then_server() {
	log=$1
	shift
	: >"$log"
	LOG="$log" PATH="$stubs:$PATH" env "$@" "$work/libexec/prepare-kernel"
	printf '%s\n' server >>"$log"
}

expect_sysctls() {
	log=$1
	printf '%s\n' \
		'modprobe br_netfilter' \
		'sysctl -w net.ipv4.ip_forward=1' \
		'sysctl -w net.bridge.bridge-nf-call-iptables=1' \
		server >"$work/expect-sysctls"
	cmp -s "$work/expect-sysctls" "$log" || {
		echo "kernel log:" >&2
		cat "$log" >&2
		fail "sysctls were not set before the server"
	}
}

expect_untouched() {
	log=$1
	printf '%s\n' server >"$work/expect-untouched"
	cmp -s "$work/expect-untouched" "$log" || {
		echo "kernel log:" >&2
		cat "$log" >&2
		fail "sysctls were written"
	}
}

missing=$work/missing-config.yaml
run_pre_then_server "$work/log-missing" EDGELET_KUBE_CONFIG_FILE="$missing"
expect_sysctls "$work/log-missing"
[ ! -e "$missing" ] || fail "missing config was created"

printf '%s\n' 'protect-kernel-defaults: false' >"$work/false.yaml"
run_pre_then_server "$work/log-false" EDGELET_KUBE_CONFIG_FILE="$work/false.yaml"
expect_sysctls "$work/log-false"

printf '%s\n' '# protect-kernel-defaults: true' 'debug: false' >"$work/comment.yaml"
run_pre_then_server "$work/log-comment" EDGELET_KUBE_CONFIG_FILE="$work/comment.yaml"
expect_sysctls "$work/log-comment"

printf '%s\n' 'protect-kernel-defaults: false # apply' >"$work/false-comment.yaml"
run_pre_then_server "$work/log-false-comment" EDGELET_KUBE_CONFIG_FILE="$work/false-comment.yaml"
expect_sysctls "$work/log-false-comment"

for spelling in true True TRUE; do
	printf '%s\n' "protect-kernel-defaults: $spelling" >"$work/true.yaml"
	run_pre_then_server "$work/log-true" EDGELET_KUBE_CONFIG_FILE="$work/true.yaml"
	expect_untouched "$work/log-true"
done

printf '%s\n' 'protect-kernel-defaults: "true"' >"$work/quoted.yaml"
run_pre_then_server "$work/log-quoted" EDGELET_KUBE_CONFIG_FILE="$work/quoted.yaml"
expect_sysctls "$work/log-quoted"

rm -rf "$work/drop.yaml.d"
mkdir -p "$work/drop.yaml.d"
printf '%s\n' 'protect-kernel-defaults: true' >"$work/drop.yaml"
printf '%s\n' 'protect-kernel-defaults: false' >"$work/drop.yaml.d/10-off.yaml"
printf '%s\n' 'protect-kernel-defaults: true' >"$work/drop.yaml.d/notes.txt"
printf '%s\n' 'protect-kernel-defaults: true' >"$work/drop.yaml.d/skip.yml"
run_pre_then_server "$work/log-drop-off" EDGELET_KUBE_CONFIG_FILE="$work/drop.yaml"
expect_sysctls "$work/log-drop-off"

rm -rf "$work/drop.yaml.d"
mkdir -p "$work/drop.yaml.d"
printf '%s\n' 'protect-kernel-defaults: false' >"$work/drop.yaml"
printf '%s\n' 'protect-kernel-defaults: false' >"$work/drop.yaml.d/10-off.yaml"
printf '%s\n' 'protect-kernel-defaults: true' >"$work/drop.yaml.d/20-on.yaml"
run_pre_then_server "$work/log-drop-on" EDGELET_KUBE_CONFIG_FILE="$work/drop.yaml"
expect_untouched "$work/log-drop-on"

rm -rf "$work/drop.yaml.d"
mkdir -p "$work/drop.yaml.d"
rm -f "$work/drop.yaml"
printf '%s\n' 'protect-kernel-defaults: true' >"$work/drop.yaml.d/10.yaml"
run_pre_then_server "$work/log-drop-only" EDGELET_KUBE_CONFIG_FILE="$work/drop.yaml"
expect_untouched "$work/log-drop-only"

printf '%s\n' 'protect-kernel-defaults: false' >"$work/false.yaml"
run_pre_then_server "$work/log-env-true" \
	EDGELET_KUBE_CONFIG_FILE="$work/false.yaml" \
	EDGELET_KUBE_PROTECT_KERNEL_DEFAULTS=true
expect_untouched "$work/log-env-true"

printf '%s\n' 'protect-kernel-defaults: true' >"$work/true.yaml"
run_pre_then_server "$work/log-env-false" \
	EDGELET_KUBE_CONFIG_FILE="$work/true.yaml" \
	EDGELET_KUBE_PROTECT_KERNEL_DEFAULTS=false
expect_sysctls "$work/log-env-false"

run_pre_then_server "$work/log-env-empty" \
	EDGELET_KUBE_CONFIG_FILE="$work/true.yaml" \
	EDGELET_KUBE_PROTECT_KERNEL_DEFAULTS=
expect_untouched "$work/log-env-empty"

# A second install replaces the binary and restarts the unit.
# SQLite, the CA, and the encryption file stay. The unit stop signals the
# server process, which stops its containerd child. The shim keeps running.
state=$work/state
run=$work/run
mkdir -p "$state/db" "$state/server/tls" "$state/server/cred" "$run"
printf '%s\n' 'sqlite-ready' >"$state/db/state.db"
printf '%s\n' 'ca-material' >"$state/server/tls/ca.crt"
printf '%s\n' 'serving-material' >"$state/server/tls/serving.crt"
printf '%s\n' 'client-admin-material' >"$state/server/tls/client-admin.crt"
printf '%s\n' 'aescbc-key' >"$state/server/cred/encryption-config.json"
cksum "$state/db/state.db" "$state/server/tls/ca.crt" \
	"$state/server/tls/serving.crt" "$state/server/tls/client-admin.crt" \
	"$state/server/cred/encryption-config.json" >"$work/kept.before"

cat >"$work/old-server.sh" <<'EOF'
#!/bin/sh
set -eu
run=$1
echo $$ > "$run/server.pid"
sleep 3600 >/dev/null 2>&1 &
echo $! > "$run/containerd.pid"
child=$!
trap 'kill "$child" 2>/dev/null || true; exit 0' TERM
nohup sleep 3600 >/dev/null 2>&1 &
echo $! > "$run/shim.pid"
while kill -0 "$child" 2>/dev/null; do
	sleep 1
done
EOF
chmod 0755 "$work/old-server.sh"
sh "$work/old-server.sh" "$run" &
echo $! >>"$work/pids"
i=0
while [ ! -s "$run/shim.pid" ]; do
	i=$((i + 1))
	if [ "$i" -gt 50 ]; then
		fail "running node did not record its shim"
	fi
	sleep 0.1
done
shim=$(cat "$run/shim.pid")
old_containerd=$(cat "$run/containerd.pid")
echo "$shim" >>"$work/pids"
echo "$old_containerd" >>"$work/pids"
kill -0 "$shim" || fail "shim is not running"
kill -0 "$old_containerd" || fail "containerd child is not running"

cat >"$work/new-bin" <<'EOF'
#!/bin/sh
set -eu
if [ "${1:-}" != "server" ]; then
	printf '%s\n' "client" "$@"
	exit 0
fi
state=${EDGELET_KUBE_STATE_DIR:?}
run=${EDGELET_KUBE_RUN_DIR:?}
[ -f "$state/db/state.db" ]
[ -f "$state/server/tls/ca.crt" ]
enc=$state/server/cred/encryption-config.json
if [ -e "$enc" ] && [ ! -f "$enc" ]; then
	echo "encryption config is not a file" >&2
	exit 1
fi
disabled=
if [ -f "$state/config.yaml" ]; then
	disabled=$(awk '
		/^disable:[ \t]+[^ \t]/ { print $2 }
		/^disable:[ \t]*$/ { on = 1; next }
		on && /^[^ \t-]/ { on = 0 }
		on && /^[ \t]*-[ \t]*/ {
			name = $0
			sub(/^[ \t]*-[ \t]*/, "", name)
			print name
		}
	' "$state/config.yaml")
fi
{
	echo Ready
	for addon in coredns local-path metrics-server traefik cert-manager; do
		skip=0
		for name in $disabled; do
			if [ "$name" = "$addon" ]; then
				skip=1
			fi
		done
		if [ "$skip" -eq 0 ]; then
			echo "$addon"
		fi
	done
} >"$run/node.status"
echo $$ >"$run/server.pid"
sleep 3600 >/dev/null 2>&1 &
echo $! >"$run/containerd.pid"
child=$!
trap 'kill "$child" 2>/dev/null || true; exit 0' TERM
while kill -0 "$child" 2>/dev/null; do
	sleep 1
done
EOF
chmod 0755 "$work/new-bin"

cat >"$work/systemctl-upgrade" <<EOF
#!/bin/sh
set -eu
printf '%s\\n' "\$*" >> "$work/upgrade-systemctl.log"
if [ "\$1" != "restart" ]; then
	exit 0
fi
mode=\$(awk -F= '/^KillMode=/ { print \$2 }' "$work/units/edgelet-kube.service")
server=\$(cat "$run/server.pid")
if [ "\$mode" = "process" ]; then
	kill -TERM "\$server"
else
	kill -TERM "\$server" \$(cat "$run/containerd.pid") \$(cat "$run/shim.pid")
fi
i=0
while kill -0 "\$server" 2>/dev/null; do
	i=\$((i + 1))
	if [ "\$i" -gt 50 ]; then
		echo "server did not stop" >&2
		exit 1
	fi
	sleep 0.1
done
EDGELET_KUBE_STATE_DIR="$state" EDGELET_KUBE_RUN_DIR="$run" \\
	"$work/bin/edgelet-kube" server >> "$run/server.log" 2>&1 &
echo \$! >> "$work/pids"
EOF
chmod 0755 "$work/systemctl-upgrade"

wait_status() {
	i=0
	while [ ! -s "$run/node.status" ]; do
		i=$((i + 1))
		if [ "$i" -gt 50 ]; then
			echo "server log:" >&2
			cat "$run/server.log" >&2 || true
			fail "node did not report status"
		fi
		sleep 0.1
	done
}

: >"$work/upgrade-systemctl.log"
rm -f "$run/node.status"
EDGELET_KUBE_BIN_DIR="$work/bin" \
EDGELET_KUBE_UNIT_DIR="$work/units" \
EDGELET_KUBE_LIBEXEC_DIR="$work/libexec" \
EDGELET_KUBE_SYSTEMCTL="$work/systemctl-upgrade" \
	sh scripts/install.sh --bin-path="$work/new-bin"
cmp -s "$work/new-bin" "$work/bin/edgelet-kube" || fail "upgrade did not replace the binary"
cksum "$state/db/state.db" "$state/server/tls/ca.crt" \
	"$state/server/tls/serving.crt" "$state/server/tls/client-admin.crt" \
	"$state/server/cred/encryption-config.json" >"$work/kept.after"
cmp -s "$work/kept.before" "$work/kept.after" || fail "database, CA, or encryption config changed"
kill -0 "$shim" || fail "unit stop killed the shim"
if kill -0 "$old_containerd" 2>/dev/null; then
	fail "containerd child survived the server stop"
fi
printf '%s\n' \
	'daemon-reload' \
	'enable edgelet-kube.service' \
	'restart edgelet-kube.service' >"$work/upgrade-systemctl.want"
cmp -s "$work/upgrade-systemctl.want" "$work/upgrade-systemctl.log" || {
	echo "upgrade systemctl log:" >&2
	cat "$work/upgrade-systemctl.log" >&2
	fail "second install did not restart the unit"
}
wait_status
printf '%s\n' Ready coredns local-path metrics-server traefik cert-manager >"$work/ready.want"
cmp -s "$work/ready.want" "$run/node.status" || {
	echo "node status:" >&2
	cat "$run/node.status" >&2
	fail "node is not Ready with the default addons"
}

# A later run still keeps the database and the CA when encryption is off,
# and a disable flag that is already set stays in effect.
rm -f "$state/server/cred/encryption-config.json"
cksum "$state/db/state.db" "$state/server/tls/ca.crt" \
	"$state/server/tls/serving.crt" "$state/server/tls/client-admin.crt" >"$work/kept-plain.before"
printf '%s\n' 'disable:' '  - metrics-server' >"$state/config.yaml"
: >"$work/upgrade-systemctl.log"
rm -f "$run/node.status"
EDGELET_KUBE_BIN_DIR="$work/bin" \
EDGELET_KUBE_UNIT_DIR="$work/units" \
EDGELET_KUBE_LIBEXEC_DIR="$work/libexec" \
EDGELET_KUBE_SYSTEMCTL="$work/systemctl-upgrade" \
	sh scripts/install.sh --bin-path="$work/new-bin"
[ ! -e "$state/server/cred/encryption-config.json" ] || fail "encryption config was created"
cksum "$state/db/state.db" "$state/server/tls/ca.crt" \
	"$state/server/tls/serving.crt" "$state/server/tls/client-admin.crt" >"$work/kept-plain.after"
cmp -s "$work/kept-plain.before" "$work/kept-plain.after" || fail "database or CA changed"
kill -0 "$shim" || fail "unit stop killed the shim"
wait_status
printf '%s\n' Ready coredns local-path traefik cert-manager >"$work/ready-disabled.want"
cmp -s "$work/ready-disabled.want" "$run/node.status" || {
	echo "node status:" >&2
	cat "$run/node.status" >&2
	fail "disabled addon was restored"
}

echo "ok"
