#!/bin/sh
# Load br_netfilter and set forwarding sysctls unless protect-kernel-defaults is true.
# A missing config file uses the default, which is false, and is not created.
set -eu

config=/etc/edgelet-kube/config.yaml
if [ -n "${EDGELET_KUBE_CONFIG_FILE:-}" ]; then
	config=$EDGELET_KUBE_CONFIG_FILE
fi

read_protect_value() {
	awk '
		/^[ \t]*#/ { next }
		/^protect-kernel-defaults:[ \t]*/ {
			line = $0
			sub(/\r$/, "", line)
			sub(/^protect-kernel-defaults:[ \t]*/, "", line)
			sub(/[ \t]+#.*/, "", line)
			gsub(/^[ \t]+|[ \t]+$/, "", line)
			found = line
		}
		END { if (found != "") print found }
	' "$1"
}

value=
if [ -f "$config" ]; then
	got=$(read_protect_value "$config")
	if [ -n "$got" ]; then
		value=$got
	fi
fi

if [ -d "$config.d" ]; then
	LC_ALL=C
	export LC_ALL
	for file in "$config.d"/*.yaml; do
		if [ -f "$file" ]; then
			got=$(read_protect_value "$file")
			if [ -n "$got" ]; then
				value=$got
			fi
		fi
	done
fi

protected=0
token=$(printf '%s' "$value" | tr -d '\r' | tr '[:upper:]' '[:lower:]')
case $token in
	true) protected=1 ;;
esac

# The environment replaces the file, and only the exact value true protects.
if [ -n "${EDGELET_KUBE_PROTECT_KERNEL_DEFAULTS:-}" ]; then
	protected=0
	if [ "$EDGELET_KUBE_PROTECT_KERNEL_DEFAULTS" = "true" ]; then
		protected=1
	fi
fi

if [ "$protected" -eq 1 ]; then
	exit 0
fi

if ! command -v modprobe >/dev/null 2>&1 || ! command -v sysctl >/dev/null 2>&1; then
	PATH="/sbin:/usr/sbin:${PATH:-/usr/bin:/bin}"
	export PATH
fi

modprobe br_netfilter
sysctl -w net.ipv4.ip_forward=1
sysctl -w net.bridge.bridge-nf-call-iptables=1
