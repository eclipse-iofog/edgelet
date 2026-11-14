#!/bin/sh
# Install the thin edgelet-kube binary, the kubectl, ctr, and crictl links,
# and edgelet-kube.service, then start it.
# A second run replaces the binary and restarts the unit.
# /var/lib/edgelet-kube/db/state.db, /var/lib/edgelet-kube/server/tls/,
# and /var/lib/edgelet-kube/server/cred/encryption-config.json are kept.
# A missing /etc/edgelet-kube/config.yaml is left missing. The node is not enrolled.
#
# Usage:
#   sudo ./install.sh
#   sudo ./install.sh --bin-path=build/edgelet-kube
#
# The thin binary defaults to ../build/edgelet-kube beside this script.
# kubectl, ctr, and crictl are symlinks to that binary. argv0 selects the command.
#
# Optional path overrides:
#   EDGELET_KUBE_BIN_DIR       destination for the thin binary (default /usr/local/bin)
#   EDGELET_KUBE_UNIT_DIR      systemd unit directory (default /etc/systemd/system)
#   EDGELET_KUBE_LIBEXEC_DIR   directory for prepare-kernel (default /usr/libexec/edgelet-kube)
#   EDGELET_KUBE_SYSTEMCTL     systemctl program (default systemctl)

set -eu

die() {
	echo "ERROR: $1" >&2
	exit 1
}

usage() {
	cat <<EOF
Usage: install.sh [--bin-path=PATH]

Installs the thin binary to /usr/local/bin/edgelet-kube and links
kubectl, ctr, and crictl to it. Installs edgelet-kube.service,
enables it, and starts it. A second run replaces the binary and
restarts the service. The database, TLS material, and encryption
config are kept.

  --bin-path=PATH   Thin binary to install.
                    Default: ../build/edgelet-kube beside this script.
  --help, -h        Show this help.
EOF
}

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
BIN_DIR=${EDGELET_KUBE_BIN_DIR:-/usr/local/bin}
UNIT_DIR=${EDGELET_KUBE_UNIT_DIR:-/etc/systemd/system}
LIBEXEC_DIR=${EDGELET_KUBE_LIBEXEC_DIR:-/usr/libexec/edgelet-kube}
SYSTEMCTL=${EDGELET_KUBE_SYSTEMCTL:-systemctl}
BIN_PATH=

for arg in "$@"; do
	case $arg in
		--bin-path=*)
			BIN_PATH=${arg#*=}
			if [ -z "$BIN_PATH" ]; then
				die "--bin-path requires a path"
			fi
			;;
		--help|-h)
			usage
			exit 0
			;;
		*)
			die "unknown option: $arg"
			;;
	esac
done

if [ -z "$BIN_PATH" ]; then
	BIN_PATH=$SCRIPT_DIR/../build/edgelet-kube
fi

unit_src=$SCRIPT_DIR/../packaging/systemd/edgelet-kube.service
kernel_src=$SCRIPT_DIR/../packaging/systemd/prepare-kernel.sh

[ -f "$BIN_PATH" ] || die "thin binary not found: $BIN_PATH"
[ -f "$unit_src" ] || die "unit file not found: $unit_src"
[ -f "$kernel_src" ] || die "kernel setup program not found: $kernel_src"

# The binary or the unit is already on disk when this is a later run.
upgrade=0
if [ -e "$BIN_DIR/edgelet-kube" ] || [ -f "$UNIT_DIR/edgelet-kube.service" ]; then
	upgrade=1
fi

echo "Installing $BIN_DIR/edgelet-kube"
mkdir -p "$BIN_DIR" "$UNIT_DIR" "$LIBEXEC_DIR"
install -m 0755 "$BIN_PATH" "$BIN_DIR/edgelet-kube"
# Same file as the thin binary. The fat runtime reads the invoked name.
echo "Linking kubectl, ctr, and crictl to $BIN_DIR/edgelet-kube"
for name in kubectl ctr crictl; do
	ln -sfn "$BIN_DIR/edgelet-kube" "$BIN_DIR/$name"
done
install -m 0755 "$kernel_src" "$LIBEXEC_DIR/prepare-kernel"
install -m 0644 "$unit_src" "$UNIT_DIR/edgelet-kube.service"

echo "Enabling edgelet-kube.service"
"$SYSTEMCTL" daemon-reload
"$SYSTEMCTL" enable edgelet-kube.service

# Restarting the unit signals the server process. That process stops its
# containerd child. Shims and pods keep running, and the new process reuses
# containerd state, SQLite, and the TLS directory.
if [ "$upgrade" -eq 1 ]; then
	echo "Restarting edgelet-kube.service"
	"$SYSTEMCTL" restart edgelet-kube.service
else
	echo "Starting edgelet-kube.service"
	"$SYSTEMCTL" start edgelet-kube.service
fi
