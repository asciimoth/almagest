#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/../.." && pwd)"
run_id="$(date -u +%Y%m%dT%H%M%SZ)-$$"
output_dir="${1:-$repo_root/artifacts/linux-isolation/$run_id}"
image="almagest-linux-isolation:${run_id,,}"

mkdir -p "$output_dir"

cleanup() {
	docker image rm --force "$image" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker build --file "$script_dir/Dockerfile" --tag "$image" "$repo_root"

docker run --rm "$image" bash -euc '
	go test -C /src/bin -race ./cmd/linux-isolation-probe ./cmd/linux-isolation-feature-probe
	go vet -C /src/bin ./cmd/linux-isolation-probe ./cmd/linux-isolation-feature-probe
'

runtime_args=(
	--rm
	--network none
	--cap-drop ALL
	--cap-add CHOWN
	--cap-add SYS_ADMIN
	--cap-add SETUID
	--cap-add SETGID
	--cap-add SETPCAP
	--security-opt seccomp=unconfined
)
docker run "${runtime_args[@]}" "$image" \
	/experiment/launcher /experiment/runtime-probe >"$output_dir/runtime.json"

device_args=()
if [[ -c /dev/net/tun ]]; then
	device_args=(--device /dev/net/tun)
fi

run_feature_profile() {
	local profile="$1"
	shift
	docker run --rm --network none --cap-drop ALL \
		--security-opt seccomp=unconfined \
		"${device_args[@]}" \
		-e "ALMAGEST_FEATURE_PROFILE=$profile" \
		"$@" "$image" /experiment/feature-probe \
		>"$output_dir/feature-$profile.json"
}

run_feature_profile ordinary --user 65534:65534
run_feature_profile net-admin --cap-add NET_ADMIN
run_feature_profile net-admin-bind --cap-add NET_ADMIN --cap-add NET_BIND_SERVICE

# CAP_BPF and CAP_PERFMON can still be insufficient for the selected backend or
# blocked by the container host. The report records that result; the profile
# never receives CAP_SYS_ADMIN as a compatibility shortcut.
run_feature_profile bpf-perfmon --cap-add BPF --cap-add PERFMON --cap-add NET_ADMIN

jq -e '
  .profile == "runtime-r-v1" and
  .landlock_abi >= 3 and
  .uid == 65534 and .gid == 65534 and .pid == 1 and
  ([.thread_ids[]] | unique | length) == 4 and
  all(.checks[]; .passed)
' "$output_dir/runtime.json" >/dev/null

jq -e '
  .profile == "ordinary" and
  .tcp_create == "available" and
  .tun_device != "available" and
  .construction_error == null
' "$output_dir/feature-ordinary.json" >/dev/null

jq -e '
  .profile == "net-admin" and
  .tcp_create == "available" and
  .construction_error == null
' "$output_dir/feature-net-admin.json" >/dev/null

{
	printf 'source_revision=%s\n' "$(git -C "$repo_root" rev-parse HEAD)"
	printf 'image=%s\n' "$image"
	printf 'docker_server=%s\n' "$(docker version --format '{{.Server.Version}}')"
	printf 'host_kernel=%s\n' "$(uname -srmo)"
	printf 'command=%q\n' "$0"
} >"$output_dir/environment.txt"

printf 'Linux isolation experiment passed. Evidence: %s\n' "$output_dir"
