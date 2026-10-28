#!/usr/bin/env bash
# Runs inside the Lima VM as root.
# A missing image leaves a new local workload queued, and does not replace a
# controller container that is already running.
set -euo pipefail

ROOT="${1:-/tmp/image-pull-failure}"
QUEUED_ID="edgelet.pull-miss-queued"
CP_ID="default.pull-miss-cp"
QUEUED_NAME="pull-miss-queued"
CP_NAME="pull-miss-cp"
MISSING_TAG="edgelet-pull-miss-absent"

fail() {
    echo "[FAIL] $*" >&2
    exit 1
}

ok() {
    echo "[ OK ] $*"
}

cleanup() {
    local rc=$?
    trap - EXIT
    edgelet --no-color controlplane delete >/dev/null 2>&1 || true
    edgelet --no-color ms rm "${QUEUED_ID}" >/dev/null 2>&1 || true
    exit "${rc}"
}
trap cleanup EXIT

edgelet --no-color controlplane delete >/dev/null 2>&1 || true
edgelet --no-color ms rm "${QUEUED_ID}" >/dev/null 2>&1 || true

systemctl is-active --quiet docker || fail "docker is not active"
command -v jq >/dev/null || fail "jq is required"

busy="$(ss -H -ltn 'sport = :80' 2>/dev/null || true; ss -H -ltn 'sport = :51121' 2>/dev/null || true)"
if [[ -n "${busy}" ]]; then
    echo "${busy}" >&2
    fail "host port 80 or 51121 is in use"
fi

inspect_doc() {
    edgelet --no-color --quiet -o json ms inspect "$1"
}

dump_inspect() {
    local id="$1"
    inspect_doc "${id}" 2>/dev/null | jq 'del(.raw, .manifestYAML)' || true
}

wait_pull_miss() {
    local id="$1" want_state="$2" i doc state pull err rc
    for i in $(seq 1 60); do
        if doc="$(inspect_doc "${id}" 2>/dev/null)"; then
            state="$(jq -r '.state // empty' <<<"${doc}")"
            pull="$(jq -r '.imagePullStatus // empty' <<<"${doc}")"
            err="$(jq -r '.errorMessage // empty' <<<"${doc}")"
            rc="$(jq -r '.restartCount // 0' <<<"${doc}")"
            if [[ "${state}" == "${want_state}" && "${pull}" == "failed" && "${err}" == *"unable to pull"* && ( "${rc}" == "0" || "${rc}" == "null" ) ]]; then
                printf '%s' "${doc}"
                return 0
            fi
        fi
        sleep 3
    done
    echo "timed out waiting for ${id}: state=${want_state} imagePullStatus=failed" >&2
    dump_inspect "${id}" >&2
    return 1
}

echo "==> local workload with an image that is not in the registry"
set +e
edgelet --no-color --timeout 4m deploy -f "${ROOT}/fixtures/queued-missing.yaml"
queued_rc=$?
set -e
echo "deploy exit ${queued_rc}"

queued_doc="$(wait_pull_miss "${QUEUED_ID}" "queued")" || fail "queued workload did not report the pull miss"
ok "local workload stayed queued with imagePullStatus failed"

queued_containers="$(docker ps -aq --filter "label=app.kubernetes.io/name=${QUEUED_NAME}")"
[[ -z "${queued_containers}" ]] || fail "queued workload created a container: ${queued_containers}"
ok "no container exists for the workload that has not started"

echo "==> controller container stays up when the new image is missing"
docker pull docker.io/library/nginx:1.27-alpine >/dev/null
edgelet --no-color --timeout 8m deploy -f "${ROOT}/fixtures/controlplane-cp.yaml"
cp_doc="$(inspect_doc "${CP_ID}")"
before_id="$(jq -r '.containerId // empty' <<<"${cp_doc}")"
before_state="$(jq -r '.state // empty' <<<"${cp_doc}")"
[[ -n "${before_id}" ]] || fail "controller container id is empty"
[[ "${before_state}" == "running" ]] || fail "controller state is ${before_state}, expected running"
running="$(docker inspect -f '{{.State.Running}}' "${before_id}")"
image="$(docker inspect -f '{{.Config.Image}}' "${before_id}")"
[[ "${running}" == "true" ]] || fail "controller container is not running"
[[ "${image}" == *"nginx"* && "${image}" == *"1.27-alpine"* ]] || fail "controller image is ${image}"
ok "controller is running ${before_id} (${image})"

set +e
edgelet --no-color --timeout 4m deploy -f "${ROOT}/fixtures/controlplane-missing.yaml"
miss_rc=$?
set -e
echo "image change deploy exit ${miss_rc}"

cp_doc="$(wait_pull_miss "${CP_ID}" "running")" || fail "running controller did not keep imagePullStatus failed"
after_id="$(jq -r '.containerId // empty' <<<"${cp_doc}")"
[[ "${after_id}" == "${before_id}" || "${after_id}" == "${before_id}"* || "${before_id}" == "${after_id}"* ]] \
    || fail "controller container changed: before=${before_id} after=${after_id}"
running="$(docker inspect -f '{{.State.Running}}' "${before_id}")"
image="$(docker inspect -f '{{.Config.Image}}' "${before_id}")"
[[ "${running}" == "true" ]] || fail "controller container stopped after the missing image"
[[ "${image}" == *"1.27-alpine"* ]] || fail "controller image changed to ${image}"
[[ "${image}" != *"${MISSING_TAG}"* ]] || fail "controller is running the missing tag"
ok "controller stayed on ${before_id} with imagePullStatus failed"

echo "==> a later retry still leaves that container running"
sleep 25
cp_doc="$(inspect_doc "${CP_ID}")"
later_id="$(jq -r '.containerId // empty' <<<"${cp_doc}")"
later_pull="$(jq -r '.imagePullStatus // empty' <<<"${cp_doc}")"
later_state="$(jq -r '.state // empty' <<<"${cp_doc}")"
[[ "${later_id}" == "${before_id}" || "${later_id}" == "${before_id}"* || "${before_id}" == "${later_id}"* ]] \
    || fail "retry replaced the controller: before=${before_id} later=${later_id}"
running="$(docker inspect -f '{{.State.Running}}' "${before_id}")"
[[ "${running}" == "true" ]] || fail "controller container stopped during retry"
[[ "${later_state}" == "running" && "${later_pull}" == "failed" ]] \
    || fail "after retry state=${later_state} imagePullStatus=${later_pull}"
ok "retry left the controller running"

echo "image pull failure checks passed"
