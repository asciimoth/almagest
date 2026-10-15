#!/usr/bin/env bash
set -euo pipefail

experiment_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$experiment_dir/../../../.." && pwd)"
runtime_image="${CAP_RUNTIME_IMAGE:-almagest-e2e:local}"
runtime_id="$(docker image inspect "$runtime_image" --format '{{.Id}}')"
run_id="capabilities-$(date +%s)-$$"
runtime_build_ref="almagest-capability-runtime:$run_id"
builder_image="${CAP_BUILDER_IMAGE:-golang@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36}"
experiment_image="${CAP_EXPERIMENT_IMAGE:-almagest-capability-experiment:$run_id}"
output_dir="${CAP_OUTPUT_DIR:-$repo_root/artifacts/isolation-design/capabilities}"
log_file="$output_dir/capability-experiment.log"
resource_label="io.almagest.experiment.run=$run_id"
probe_image=""
probe_timeout="${CAP_PROBE_TIMEOUT:-30s}"

mkdir -p "$output_dir"
: >"$log_file"

record() {
	printf '%s\n' "$*" | tee -a "$log_file"
}

cleanup() {
	local container
	while IFS= read -r container; do
		[[ -n "$container" ]] || continue
		docker rm --force "$container" >/dev/null 2>&1 || true
	done < <(docker ps --all --quiet --filter "label=$resource_label" 2>/dev/null || true)
	docker image rm "$experiment_image" "$runtime_build_ref" >/dev/null 2>&1 || true
}
trap cleanup EXIT
trap 'exit 130' INT TERM

run_probe() {
	local name="$1"
	local mode="$2"
	shift 2
	record "CASE $name"
	record "COMMAND timeout --signal=TERM --kill-after=5s $probe_timeout docker run --rm --network none --label $resource_label --device /dev/net/tun $* $probe_image $mode"
	set +e
	timeout --signal=TERM --kill-after=5s "$probe_timeout" \
		docker run --rm --network none --label "$resource_label" --device /dev/net/tun "$@" "$probe_image" "$mode" \
		2>&1 | tee -a "$log_file"
	local status=${PIPESTATUS[0]}
	set -e
	record "EXIT $name $status"
	if ((status != 0)); then
		return "$status"
	fi
}

record "START $(date --iso-8601=seconds)"
record "HOST_KERNEL $(uname -srvm)"
record "DOCKER $(docker version --format 'client={{.Client.Version}} server={{.Server.Version}} api={{.Server.APIVersion}} os={{.Server.Os}} arch={{.Server.Arch}}')"
record "RUNTIME source=$runtime_image $(docker image inspect "$runtime_id" --format 'id={{.Id}} os={{.Os}} arch={{.Architecture}} input={{index .Config.Labels "io.almagest.e2e.input-sha256"}}')"
record "BUILDER $(docker image inspect "$builder_image" --format 'id={{.Id}} digests={{join .RepoDigests ","}}')"
docker image tag "$runtime_id" "$runtime_build_ref"
record "RUNTIME_BUILD_REF $runtime_build_ref=$(docker image inspect "$runtime_build_ref" --format '{{.Id}}')"
record "BUILD docker build --network none --label $resource_label --build-arg RUNTIME_IMAGE=$runtime_build_ref --build-arg BUILDER_IMAGE=$builder_image -t $experiment_image $experiment_dir"
docker build --network none \
	--label "$resource_label" \
	--build-arg "RUNTIME_IMAGE=$runtime_build_ref" \
	--build-arg "BUILDER_IMAGE=$builder_image" \
	-t "$experiment_image" "$experiment_dir" 2>&1 | tee -a "$log_file"
probe_image="$(docker image inspect "$experiment_image" --format '{{.Id}}')"
record "EXPERIMENT id=$probe_image"
record "FILE_CAP $(timeout --signal=TERM --kill-after=5s "$probe_timeout" docker run --rm --network none --label "$resource_label" --entrypoint /usr/sbin/getcap "$probe_image" /usr/local/bin/cap-probe-file)"

# The bootstrap starts with only the capabilities needed to establish the child
# identity and capability sets. It is an external service-manager analogue.
run_probe ambient bootstrap \
	--user 0:0 --cap-drop ALL \
	--cap-add NET_ADMIN --cap-add SETUID --cap-add SETGID --cap-add SETPCAP \
	--entrypoint /usr/local/bin/cap-probe

# no_new_privs does not remove an ambient capability that the trusted bootstrap
# establishes from an already permitted and inheritable capability.
run_probe ambient-nnp bootstrap \
	--user 0:0 --cap-drop ALL \
	--cap-add NET_ADMIN --cap-add SETUID --cap-add SETGID --cap-add SETPCAP \
	--security-opt no-new-privileges \
	--entrypoint /usr/local/bin/cap-probe

# A file capability supplies CAP_NET_ADMIN to a non-root process when the
# external bounding set permits it.
run_probe file-cap file-1 \
	--user 65534:65534 --cap-drop ALL --cap-add NET_ADMIN \
	--env EXPECT_CAP=1 --env EXPECT_IP=denied \
	--entrypoint /usr/local/bin/cap-probe-file

# no_new_privs blocks acquisition from the file capability.
run_probe file-cap-nnp nnp-file-1 \
	--user 65534:65534 --cap-drop ALL --cap-add NET_ADMIN \
	--security-opt no-new-privileges --env EXPECT_TUN=denied --env EXPECT_IP=denied \
	--entrypoint /usr/local/bin/cap-probe

run_probe no-cap none \
	--user 65534:65534 --cap-drop ALL --env EXPECT_TUN=denied --env EXPECT_IP=denied \
	--entrypoint /usr/local/bin/cap-probe

# An externally imposed filter can remove TUN ioctl authority while leaving
# CAP_NET_ADMIN and netlink dummy/route operations available.
run_probe ambient-seccomp-deny-ioctl bootstrap \
	--user 0:0 --cap-drop ALL \
	--cap-add NET_ADMIN --cap-add SETUID --cap-add SETGID --cap-add SETPCAP \
	--security-opt "seccomp=$experiment_dir/seccomp-deny-ioctl.json" \
	--env EXPECT_TUN=denied \
	--entrypoint /usr/local/bin/cap-probe

record "END $(date --iso-8601=seconds)"
record "LOG $log_file"
