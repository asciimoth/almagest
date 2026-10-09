#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/../.." && pwd)
run_id=$(date -u +%Y%m%dT%H%M%SZ)-$$
output_dir=${1:-$repo_root/artifacts/data-path/security-$run_id}
image="almagest-data-security:${run_id,,}"
container="almagest-data-security-${run_id,,}"

mkdir -p "$output_dir"
cleanup() {
	docker container rm --force "$container" >/dev/null 2>&1 || true
	docker image rm --force "$image" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker build --file "$repo_root/dev/linux-isolation/Dockerfile" --tag "$image" "$repo_root"
timeout 90s docker run --name "$container" --rm --network none \
	--cap-drop ALL --cap-add CHOWN --cap-add SYS_ADMIN --cap-add SETUID \
	--cap-add SETGID --cap-add SETPCAP --cap-add NET_ADMIN \
	--security-opt seccomp=unconfined --device /dev/net/tun \
	"$image" /experiment/launcher --broker /experiment/security-probe /experiment/security-probe \
	>"$output_dir/linux.json" 2>"$output_dir/linux.log"
python3 "$script_dir/security-evidence.py" "$output_dir/linux.json"

{
	printf 'source_revision=%s\n' "$(git -C "$repo_root" rev-parse HEAD)"
	printf 'source_dirty=%s\n' "$(test -n "$(git -C "$repo_root" status --short)" && echo yes || echo no)"
	printf 'source_sha256='
	sha256sum "$script_dir/security.c"
	printf 'docker_server=%s\n' "$(docker version --format '{{.Server.Version}}')"
	printf 'host_kernel=%s\n' "$(uname -srmo)"
	printf 'command=%q\n' "$0"
} >"$output_dir/environment.txt"
printf 'Hostile-client audit passed (candidate rejection evidence): %s\n' "$output_dir"
