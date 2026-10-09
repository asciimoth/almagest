#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/../.." && pwd)
run_id=$(date -u +%Y%m%dT%H%M%SZ)-$$
output_dir=${1:-$repo_root/artifacts/data-path/$run_id}
image="almagest-data-path:${run_id,,}"

mkdir -p "$output_dir"
cleanup() { docker image rm --force "$image" >/dev/null 2>&1 || true; }
trap cleanup EXIT

docker build --file "$repo_root/dev/linux-isolation/Dockerfile" --tag "$image" "$repo_root"
docker run --rm --network none "$image" bash -euc '
	packages=(
		./cmd/linux-isolation-data-path-probe
		./cmd/linux-isolation-matcher-broker
		./cmd/linux-isolation-matcher-probe
		./cmd/linux-isolation-tun-broker
		./cmd/linux-isolation-tun-probe
		./internal/isolation/matcherexperiment
	)
	go test -C /src/bin -race "${packages[@]}"
	go vet -C /src/bin "${packages[@]}"
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
	-e "ALMAGEST_DATA_PATH_RUNS=${ALMAGEST_DATA_PATH_RUNS:-7}"
	-e "ALMAGEST_DATA_PATH_SAMPLES=${ALMAGEST_DATA_PATH_SAMPLES:-200}"
)
if [[ ! -c /dev/net/tun ]]; then
	printf 'Host /dev/net/tun is unavailable; native TUN evidence is required.\n' >&2
	exit 1
fi
for log_mode in disabled normal overloaded; do
	docker run "${runtime_args[@]}" -e "ALMAGEST_LOG_LOAD=$log_mode" "$image" \
		/experiment/launcher --benchmark /experiment/data-path-probe \
		>"$output_dir/linux-$log_mode.json" 2>"$output_dir/linux-$log_mode.log"
	docker run "${runtime_args[@]}" -e "ALMAGEST_LOG_LOAD=$log_mode" \
		-e "ALMAGEST_MATCHER_SAMPLES=${ALMAGEST_MATCHER_SAMPLES:-200}" \
		"$image" /experiment/launcher --broker /experiment/matcher-broker /experiment/matcher-probe \
		>"$output_dir/linux-matcher-$log_mode.json" 2>"$output_dir/linux-matcher-$log_mode.log"
	docker run "${runtime_args[@]}" -e "ALMAGEST_LOG_LOAD=$log_mode" \
		--cap-add NET_ADMIN --device /dev/net/tun \
		-e "ALMAGEST_TUN_SAMPLES=${ALMAGEST_TUN_SAMPLES:-100}" \
		"$image" /experiment/launcher --broker /experiment/tun-broker /experiment/tun-probe \
		>"$output_dir/linux-tun-$log_mode.json" 2>"$output_dir/linux-tun-$log_mode.log"

	jq -e --arg mode "$log_mode" '
  .schema_version == 1 and .profile == "linux-runtime-r-v1" and
  .log_mode == $mode and
  .runs >= 7 and .samples_per_run >= 1 and
  .restrictions.uid == 65534 and .restrictions.gid == 65534 and
  .restrictions.pid == 1 and .restrictions.no_new_privileges and
  .restrictions.seccomp_mode == 2 and .restrictions.native_socket_denied and
  .restrictions.final_filter_applied and .restrictions.private_channel_works and
  .semantics.packet_boundaries and .semantics.payload_integrity and
  ([.measurements[] | .candidate] | unique | sort) == ["broker-configured-direct-handle", "broker-relay", "in-process-native", "shared-memory-relay"] and
  ([.measurements[] | .packet_bytes] | unique | sort) == [64, 256, 512, 1200, 1500] and
  ([.measurements[] | .batch_size] | unique | sort) == [1, 16] and
  .control_response_p99_ns > 0 and .saturated_data_completed
' "$output_dir/linux-$log_mode.json" >/dev/null

  jq -e --arg mode "$log_mode" '
  .schema_version == 1 and .runs >= 7 and .native_socket_denied and
  .log_mode == $mode and
  .malformed_rejected and .fresh_tuple_accepted and .no_observation_lease and
  ([.measurements[] | .candidate] | unique | sort) == ["bounded-matcher-rpc", "in-process-native"] and
  ([.measurements[] | .flow] | unique | sort) == ["tcp4", "tcp6", "udp4", "udp6"]
' "$output_dir/linux-matcher-$log_mode.json" >/dev/null

  jq -e --arg mode "$log_mode" '
  .schema_version == 1 and .runs >= 7 and .native_socket_denied and .payload_integrity and
  .log_mode == $mode and
  ([.measurements[] | .candidate] | unique | sort) == ["broker-configured-direct-handle", "broker-relay", "in-process-native", "shared-memory-relay"] and
  ([.measurements[] | .family] | unique | sort) == ["ipv4", "ipv6"] and
  ([.measurements[] | .workload] | unique | sort) == ["plain", "wireguard-shaped"] and
  ([.measurements[] | .packet_bytes] | unique | sort) == [64, 256, 512, 1200, 1500]
' "$output_dir/linux-tun-$log_mode.json" >/dev/null
done

analysis_inputs=()
for log_mode in disabled normal overloaded; do
	analysis_inputs+=("$output_dir/linux-$log_mode.json" "$output_dir/linux-matcher-$log_mode.json" "$output_dir/linux-tun-$log_mode.json")
done
python3 "$script_dir/analyze.py" linux "${analysis_inputs[@]}" --output "$output_dir/analysis-linux.json"

{
	printf 'source_revision=%s\n' "$(git -C "$repo_root" rev-parse HEAD)"
	printf 'source_dirty=%s\n' "$(test -n "$(git -C "$repo_root" status --short)" && echo yes || echo no)"
	printf 'docker_server=%s\n' "$(docker version --format '{{.Server.Version}}')"
	printf 'host_kernel=%s\n' "$(uname -srmo)"
	printf 'runs=%s\n' "${ALMAGEST_DATA_PATH_RUNS:-7}"
	printf 'samples_per_run=%s\n' "${ALMAGEST_DATA_PATH_SAMPLES:-200}"
	printf 'command=%q\n' "$0"
} >"$output_dir/environment.txt"

printf 'Data-path experiment passed. Evidence: %s\n' "$output_dir"
