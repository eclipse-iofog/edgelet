#!/usr/bin/env bash
# Package edgelet binary-only release artifacts.
# Run from repo root: ./scripts/release-binaries.sh [VERSION]
# Requires build/edgelet-<os>-<arch>[.exe] from make build-all-archs / build-desktop-*.

set -e

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

VERSION="${1:-$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)}"
OUT="${ROOT}/dist"
ETC="${ROOT}/packaging/edgelet/etc/edgelet"

mkdir -p "$OUT"
rm -f "${OUT}"/edgelet-* "${OUT}"/SHA256SUMS \
    "${OUT}/edgelet-config.yaml.sample" \
    "${OUT}/edgelet-controller-ca.crt.sample" \
    "${OUT}/install.sh" "${OUT}/uninstall.sh" \
    "${OUT}/LICENSE-3RD-PARTY.md"

copy_binary() {
  local _src="$1" _dst="$2"
  [ -f "$_src" ] || return 0
  cp "$_src" "$_dst"
  chmod 755 "$_dst" 2>/dev/null || true
  echo "Wrote ${_dst}"
  copy_sbom "$(basename "$_dst")"
}

copy_sbom() {
  local _base="$1" _ext
  for _ext in cdx.json spdx.json; do
    local _src="${ROOT}/build/${_base}.${_ext}"
    if [ ! -f "${_src}" ]; then
      echo "ERROR: missing ${_src}" >&2
      echo "Generate it with: cd licensegen && go run . -root .. -release -binary ../build/${_base} -version <tag> -out ../build" >&2
      exit 1
    fi
    cp "${_src}" "${OUT}/${_base}.${_ext}"
    echo "Wrote ${OUT}/${_base}.${_ext}"
  done
}

for _arch in amd64 arm64 arm riscv64; do
  copy_binary "${ROOT}/build/edgelet-linux-${_arch}" "${OUT}/edgelet-linux-${_arch}"
done

copy_binary "${ROOT}/build/edgelet-darwin-amd64" "${OUT}/edgelet-darwin-amd64"
copy_binary "${ROOT}/build/edgelet-darwin-arm64" "${OUT}/edgelet-darwin-arm64"
copy_binary "${ROOT}/build/edgelet-windows-amd64.exe" "${OUT}/edgelet-windows-amd64.exe"

if [ -f "${ETC}/config.default.yaml" ]; then
  cp "${ETC}/config.default.yaml" "${OUT}/edgelet-config.yaml.sample"
  echo "Wrote ${OUT}/edgelet-config.yaml.sample"
fi

if [ -f "${ETC}/controller-ca.sample.crt" ]; then
  cp "${ETC}/controller-ca.sample.crt" "${OUT}/edgelet-controller-ca.crt.sample"
  echo "Wrote ${OUT}/edgelet-controller-ca.crt.sample"
fi

cp "${ROOT}/install.sh" "${OUT}/install.sh"
cp "${ROOT}/uninstall.sh" "${OUT}/uninstall.sh"
chmod 755 "${OUT}/install.sh" "${OUT}/uninstall.sh"
echo "Wrote ${OUT}/install.sh and ${OUT}/uninstall.sh"

cp "${ROOT}/LICENSE-3RD-PARTY.md" "${OUT}/LICENSE-3RD-PARTY.md"
echo "Wrote ${OUT}/LICENSE-3RD-PARTY.md"

(
  cd "$OUT"
  sha256sum edgelet-* edgelet-config.yaml.sample edgelet-controller-ca.crt.sample \
    install.sh uninstall.sh LICENSE-3RD-PARTY.md 2>/dev/null > SHA256SUMS || true
)

echo "Release ${VERSION}: checksums at ${OUT}/SHA256SUMS"
