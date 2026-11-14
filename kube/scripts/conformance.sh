#!/usr/bin/env bash
# Install one linux/amd64 node from the release binary and the matching
# airgap archive, then run sonobuoy in certified-conformance mode.
# The default addons stay installed. A second node is not added.
#
# Usage:
#   sudo ./conformance.sh \
#     --binary=dist/edgelet-kube-linux-amd64 \
#     --images=dist/edgelet-kube-airgap-images-amd64.tar.zst
#
# CONFORMANCE_TIMEOUT_MINUTES is the job time limit in minutes (default 240).
# The sonobuoy run stops 45 minutes before that limit so install and
# retrieval finish inside it. RELEASE_NAME, when it is a release tag,
# must name the same Kubernetes patch the binary reports.

set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
KUBECONFIG_PATH=/etc/edgelet-kube/edgelet-kube.yaml
IMAGES_DIR=/var/lib/edgelet-kube/agent/images
BINARY_NAME=edgelet-kube-linux-amd64
IMAGES_NAME=edgelet-kube-airgap-images-amd64.tar.zst
KUBECTL=/usr/local/bin/kubectl
SONOBUOY_VERSION=0.57.5
SONOBUOY_SUM=7c5c2250e5103c98f4dad2bcab86baf954e319c57c419c113f28f5060e62f129

die() {
	echo "ERROR: $1" >&2
	exit 1
}

binary=
images=
for arg in "$@"; do
	case $arg in
	--binary=*)
		binary=${arg#*=}
		;;
	--images=*)
		images=${arg#*=}
		;;
	--help|-h)
		sed -n '2,12p' "$0"
		exit 0
		;;
	*)
		die "unknown option: $arg"
		;;
	esac
done

[ -n "$binary" ] || die "--binary is required"
[ -n "$images" ] || die "--images is required"
[ -f "$binary" ] || die "release binary not found: $binary"
[ -f "$images" ] || die "airgap archive not found: $images"
[ "$(basename "$binary")" = "$BINARY_NAME" ] || die "binary must be named $BINARY_NAME"
[ "$(basename "$images")" = "$IMAGES_NAME" ] || die "airgap archive must be named $IMAGES_NAME"
[ "$(uname -m)" = x86_64 ] || die "the conformance node is linux/amd64"

if [ "${SONOBUOY_MODE:-certified-conformance}" != certified-conformance ]; then
	die "mode must be certified-conformance"
fi

minutes=${CONFORMANCE_TIMEOUT_MINUTES:-240}
case $minutes in
''|*[!0-9]*) die "CONFORMANCE_TIMEOUT_MINUTES must be a number of minutes" ;;
esac
# Minutes kept for install and for retrieving results.
reserve_minutes=45
if [ "$minutes" -le "$reserve_minutes" ]; then
	die "CONFORMANCE_TIMEOUT_MINUTES must be greater than ${reserve_minutes}"
fi
wait_minutes=$((minutes - reserve_minutes))
timeout_seconds=$((wait_minutes * 60))

pin=$(awk '$1 == "kubernetes" { print $2; exit }' "$SCRIPT_DIR/airgap/pins.txt")
[ -n "$pin" ] || die "kubernetes patch is not set"
release_name=${RELEASE_NAME:-}
case $release_name in
edgelet-kube-v*+edgelet*)
	named=${release_name#edgelet-kube-}
	named=${named%%+*}
	named=${named%%-*}
	[ "$named" = "$pin" ] || die "patch ${named} in ${release_name} is not ${pin}"
	;;
esac

command -v go >/dev/null 2>&1 || die "go is required to read the binary version"
command -v curl >/dev/null 2>&1 || die "curl is required"
command -v file >/dev/null 2>&1 || die "file is required"
file -b "$binary" | grep -q 'ELF 64-bit LSB executable, x86-64' || die "binary is not a linux/amd64 executable"

if [ "$(id -u)" -ne 0 ]; then
	exec sudo --preserve-env=CONFORMANCE_TIMEOUT_MINUTES,SONOBUOY_MODE,RELEASE_NAME,CONFORMANCE_RESULTS,PATH \
		env PATH="$PATH" bash "$0" "$@"
fi

refuse_disabled_addon() {
	local file=$1
	awk '
		/^[[:space:]]*#/ { next }
		/^[[:space:]]*disable\+?:/ { on = 1 }
		on && /^[^[:space:]#]/ && !/^[[:space:]]*disable\+?:/ { on = 0 }
		on && /traefik|cert-manager|metrics-server|local-storage|local-path/ { found = 1 }
		END { exit found ? 0 : 1 }
	' "$file"
}

shopt -s nullglob
for config in /etc/edgelet-kube/config.yaml /etc/edgelet-kube/config.yaml.d/*.yaml; do
	if [ -f "$config" ] && refuse_disabled_addon "$config"; then
		die "default addons are turned off in ${config}"
	fi
done
shopt -u nullglob

if swapon --show 2>/dev/null | grep -q .; then
	swapoff -a
fi

echo "Installing ${IMAGES_NAME}"
mkdir -p "$IMAGES_DIR"
install -m 0644 "$images" "${IMAGES_DIR}/${IMAGES_NAME}"

echo "Installing ${BINARY_NAME}"
bash "$SCRIPT_DIR/install.sh" --bin-path="$binary"

export KUBECONFIG=$KUBECONFIG_PATH

wait_until() {
	local label=$1 timeout=$2
	shift 2
	local elapsed=0
	while true; do
		if "$@"; then
			return 0
		fi
		if [ "$elapsed" -ge "$timeout" ]; then
			echo "ERROR: timed out waiting for ${label}" >&2
			return 1
		fi
		sleep 5
		elapsed=$((elapsed + 5))
	done
}

node_ready() {
	[ -f "$KUBECONFIG_PATH" ] || return 1
	local nodes count ready
	nodes=$("$KUBECTL" get nodes --no-headers 2>/dev/null || true)
	count=$(printf '%s\n' "$nodes" | awk 'NF { c++ } END { print c+0 }')
	ready=$(printf '%s\n' "$nodes" | awk '$2 == "Ready" { c++ } END { print c+0 }')
	if [ "$count" -gt 1 ]; then
		die "conformance target is one node"
	fi
	[ "$count" -eq 1 ] && [ "$ready" -eq 1 ]
}

echo "Waiting for the node"
wait_until "the node" 1200 node_ready

deploy_ready() {
	local ns=$1 name=$2 ready want
	"$KUBECTL" -n "$ns" get deploy "$name" >/dev/null 2>&1 || return 1
	ready=$("$KUBECTL" -n "$ns" get deploy "$name" -o jsonpath='{.status.readyReplicas}')
	want=$("$KUBECTL" -n "$ns" get deploy "$name" -o jsonpath='{.spec.replicas}')
	[ -n "$ready" ] && [ "$ready" = "$want" ] && [ "$ready" != 0 ]
}

wait_deploy() {
	local ns=$1 name=$2
	echo "Waiting for deployment ${ns}/${name}"
	if ! wait_until "deployment ${ns}/${name}" 900 deploy_ready "$ns" "$name"; then
		"$KUBECTL" get deploy -A -o wide || true
		exit 1
	fi
}

wait_deploy kube-system coredns
wait_deploy kube-system traefik
wait_deploy kube-system metrics-server
wait_deploy kube-system local-path-provisioner
wait_deploy cert-manager cert-manager

count=$("$KUBECTL" get nodes --no-headers | awk 'NF { c++ } END { print c+0 }')
[ "$count" -eq 1 ] || die "conformance target is one node"

got=$("$KUBECTL" version -o jsonpath='{.serverVersion.gitVersion}')
[ "$got" = "$pin" ] || die "gitVersion ${got} is not ${pin}"
if [ -n "$release_name" ] && [ "$got" = "$release_name" ]; then
	die "gitVersion is the release name"
fi

fat=$(find /var/lib/edgelet-kube/data -type f -path '*/bin/edgelet-kube' -print -quit)
[ -n "$fat" ] || die "fat runtime was not extracted"
info=$(go version -m "$fat")
count=$(printf '%s\n' "$info" | grep -o -F "version.gitVersion=${pin}" | wc -l | tr -d ' ' || true)
[ "$count" -ge 2 ] || die "binary gitVersion is not ${pin}"
if [ -n "$release_name" ] && printf '%s\n' "$info" | grep -F -q "gitVersion=${release_name}"; then
	die "release name was stamped as gitVersion"
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
archive="sonobuoy_${SONOBUOY_VERSION}_linux_amd64.tar.gz"
curl -fsSL -o "$tmp/$archive" \
	"https://github.com/vmware-tanzu/sonobuoy/releases/download/v${SONOBUOY_VERSION}/${archive}"
echo "${SONOBUOY_SUM}  ${tmp}/${archive}" | sha256sum -c -
tar -C "$tmp" -xzf "$tmp/$archive" sonobuoy
install -m 0755 "$tmp/sonobuoy" /usr/local/bin/sonobuoy

results=${CONFORMANCE_RESULTS:-/var/tmp/sonobuoy-results.tar.gz}
mkdir -p "$(dirname "$results")"
echo "Running sonobuoy --mode certified-conformance (wait ${wait_minutes}m, job limit ${minutes}m)"
status=0
/usr/local/bin/sonobuoy run \
	--mode=certified-conformance \
	--kubeconfig="$KUBECONFIG_PATH" \
	--kubernetes-version="$pin" \
	--timeout="$timeout_seconds" \
	--wait="$wait_minutes" \
	--wait-output=progress || status=$?

if /usr/local/bin/sonobuoy retrieve --kubeconfig="$KUBECONFIG_PATH" --filename "$results"; then
	/usr/local/bin/sonobuoy results "$results" || status=$?
else
	status=1
fi
exit "$status"
