#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
fixture="$root/e2e/experiments/linux_adaptive_isolation/rootless"
output="${ROOTLESS_OUTPUT:-$root/artifacts/isolation-design/rootless}"
runtime_image="${ROOTLESS_RUNTIME_IMAGE:-almagest-e2e:local}"
builder_image="${ROOTLESS_BUILDER_IMAGE:-golang:1.26.9-bookworm}"
probe_image="${ROOTLESS_PROBE_IMAGE:-almagest-rootless-isolation-probe:local}"
run_id="rootless-$(date -u +%Y%m%dT%H%M%SZ)-$$"
resource_label="io.almagest.experiment.rootless=$run_id"

mkdir -p "$output"

cleanup() {
	local container
	while IFS= read -r container; do
		[[ -n "$container" ]] || continue
		docker rm --force "$container" >/dev/null 2>&1 || true
	done < <(docker ps -aq --filter "label=$resource_label")
}
trap cleanup EXIT
trap 'exit 130' INT TERM

runtime_id="$(docker image inspect "$runtime_image" --format '{{.Id}}')"
builder_id="$(docker image inspect "$builder_image" --format '{{.Id}}')"
{
	printf 'date_utc=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
	printf 'kernel=%s\n' "$(uname -srvm)"
	printf 'docker_server_version=%s\n' "$(docker version --format '{{.Server.Version}}')"
	printf 'docker_security_options=%s\n' "$(docker info --format '{{json .SecurityOptions}}')"
	printf 'runtime_image=%s\n' "$runtime_image"
	printf 'runtime_id=%s\n' "$runtime_id"
	printf 'builder_image=%s\n' "$builder_image"
	printf 'builder_id=%s\n' "$builder_id"
} >"$output/environment.txt"

docker build \
	--build-arg "RUNTIME_IMAGE=$runtime_image" \
	--build-arg "BUILDER_IMAGE=$builder_image" \
	-f "$fixture/Dockerfile" \
	-t "$probe_image" \
	"$fixture" >"$output/build.log" 2>&1
[[ "$(docker image inspect "$runtime_image" --format '{{.Id}}')" == "$runtime_id" ]]
[[ "$(docker image inspect "$builder_image" --format '{{.Id}}')" == "$builder_id" ]]
docker image inspect "$probe_image" --format '{{.Id}}' >"$output/probe-image-id.txt"

run_profile() {
	local name="$1"
	shift
	local status=0
	local cidfile="$output/$name.cid"
	rm -f "$cidfile"
	set +e
	timeout --signal=TERM --kill-after=5s 30s docker run --rm \
		--cidfile "$cidfile" \
		--label "$resource_label" \
		--network none \
		--user 65534:65534 \
		--cap-drop ALL \
		--security-opt no-new-privileges \
		"$@" \
		"$probe_image" >"$output/$name.log" 2>&1
	status=$?
	set -e
	if [[ -s "$cidfile" ]]; then
		docker rm --force "$(<"$cidfile")" >/dev/null 2>&1 || true
	fi
	rm -f "$cidfile"
	printf '%s\n' "$status" >"$output/$name.exit"
	printf '%s exit=%d\n' "$name" "$status"
}

run_profile docker-default
run_profile seccomp-unconfined --security-opt seccomp=unconfined

if [[ "$(<"$output/docker-default.exit")" != 0 || "$(<"$output/seccomp-unconfined.exit")" != 0 ]]; then
	echo "one or more rootless probes failed" >&2
	exit 1
fi
