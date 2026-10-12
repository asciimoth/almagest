#!/usr/bin/env bash
set -euo pipefail

: "${ALMAGEST_E2E_TIMEOUT:=45}"
source /e2e/lib/common.sh

daemon_names=(a b c)
declare -A daemon_pids=()
declare -A daemon_cgroups=()
declare -A admin_socks=(
	[a]="$E2E_RUNTIME/daemon-a.sock"
	[b]="$E2E_RUNTIME/daemon-b.sock"
	[c]="$E2E_RUNTIME/daemon-c.sock"
)
declare -A daemon_logs=(
	[a]="$E2E_RUNTIME/daemon-a.log"
	[b]="$E2E_RUNTIME/daemon-b.log"
	[c]="$E2E_RUNTIME/daemon-c.log"
)

client_menu="/tmp/e2e-wireguard-amnezia-client-menu.json"
capture_file="$E2E_RUNTIME/wireguard-outer-traffic.pcapng"
capture_pid=""

fail() {
	log "FAILED: $*"
	for name in "${daemon_names[@]}"; do
		local daemon_log="${daemon_logs[$name]}"
		if [[ -f "$daemon_log" ]]; then
			log "$(basename "$daemon_log") follows"
			tail -200 "$daemon_log" >&2 || true
		fi
	done
	exit 1
}

cli_daemon() {
	local name="$1"
	shift
	almagest-cli --url "unix://${admin_socks[$name]}" "$@"
}

stop_daemons() {
	trap - EXIT
	stop_capture
	for name in "${daemon_names[@]}"; do
		local pid="${daemon_pids[$name]:-}"
		if [[ -z "$pid" ]]; then
			continue
		fi
		if kill -0 "$pid" 2>/dev/null; then
			log "stopping daemon $name"
			kill "$pid" 2>/dev/null || true
			local deadline=$((SECONDS + 10))
			while kill -0 "$pid" 2>/dev/null && ((SECONDS <= deadline)); do
				sleep 0.25
			done
			if kill -0 "$pid" 2>/dev/null; then
				kill -KILL "$pid" 2>/dev/null || true
			fi
		fi
		wait "$pid" 2>/dev/null || true
		daemon_pids[$name]=""
		if [[ -n "${daemon_cgroups[$name]:-}" ]]; then
			rmdir "${daemon_cgroups[$name]}" 2>/dev/null || true
			daemon_cgroups[$name]=""
		fi
	done
}

stop_capture() {
	if [[ -z "${capture_pid:-}" ]]; then
		return
	fi
	if kill -0 "$capture_pid" 2>/dev/null; then
		kill -INT "$capture_pid" 2>/dev/null || true
	fi
	wait "$capture_pid" 2>/dev/null || true
	capture_pid=""
}

capture_report() {
	local port="$1"
	local report="$2"
	tshark -n -r "$capture_file" -Y "udp.port == $port" -T fields -e udp.payload \
		| awk '
			BEGIN { total=0; initiation=0; response=0; cookie=0; transport=0 }
			{
				payload=$0
				gsub(":", "", payload)
				if (payload == "") next
				total++
				payload_len=length(payload)/2
				prefix=substr(payload, 1, 8)
				if (prefix == "01000000" && payload_len == 148) initiation++
				else if (prefix == "02000000" && payload_len == 92) response++
				else if (prefix == "03000000" && payload_len == 64) cookie++
				else if (prefix == "04000000" && payload_len >= 32) transport++
			}
			END {
				printf "packet_count=%d\n", total
				printf "initiation_packets=%d\n", initiation
				printf "response_packets=%d\n", response
				printf "cookie_packets=%d\n", cookie
				printf "transport_packets=%d\n", transport
				printf "wireguard_signature_packets=%d\n", initiation+response+cookie+transport
			}
		' >"$report"
}

report_value() {
	local report="$1"
	local key="$2"
	awk -F= -v key="$key" '$1 == key { print $2 }' "$report"
}

start_daemon_named() {
	local name="$1"
	local daemon_log="${daemon_logs[$name]}"
	local admin_sock="${admin_socks[$name]}"

	log "starting daemon $name"
	local pid_var="daemon_pid_$name"
	local cgroup_var="daemon_cgroup_$name"
	local "$pid_var"=""
	local "$cgroup_var"=""
	start_isolated_daemon_instance "$name" "$E2E_CONFIG_DIR/daemon-$name" "$admin_sock" \
		"$daemon_log" "$pid_var" "$cgroup_var"
	daemon_pids[$name]="${!pid_var}"
	daemon_cgroups[$name]="${!cgroup_var}"
	trap stop_daemons EXIT
	retry "$ALMAGEST_E2E_TIMEOUT" "daemon $name admin API ping" \
		cli_daemon "$name" admin ping --nonce "$E2E_CASE-$name" \
		>"/tmp/e2e-wireguard-amnezia-ping-$name.json" \
		|| fail "daemon $name admin API did not answer ping"
	jq -e --arg nonce "$E2E_CASE-$name" '.nonce == $nonce' \
		"/tmp/e2e-wireguard-amnezia-ping-$name.json" >/dev/null \
		|| fail "daemon $name admin API ping returned unexpected payload"
}

node_id_by_name_daemon() {
	local daemon="$1"
	local name="$2"
	cli_daemon "$daemon" graph map | jq -er --arg name "$name" '.nodes[$name].ID'
}

wait_graph_node_daemon() {
	local daemon="$1"
	local name="$2"
	retry "$ALMAGEST_E2E_TIMEOUT" "daemon $daemon graph node $name" \
		node_id_by_name_daemon "$daemon" "$name" >/dev/null
}

node_label_daemon() {
	local daemon="$1"
	local id="$2"
	cli_daemon "$daemon" graph node --id "$id" | jq -er '.snapshot.label'
}

node_label_is_daemon() {
	local daemon="$1"
	local id="$2"
	local label="$3"
	[[ "$(node_label_daemon "$daemon" "$id")" == "$label" ]]
}

wait_node_label_daemon() {
	local daemon="$1"
	local id="$2"
	local label="$3"
	retry "$ALMAGEST_E2E_TIMEOUT" "daemon $daemon node $id label $label" \
		node_label_is_daemon "$daemon" "$id" "$label"
}

log_contains_daemon() {
	local daemon="$1"
	local class="$2"
	local text="$3"
	cli_daemon "$daemon" log subscribe | jq -e --arg class "$class" --arg text "$text" \
		'.entries[] | select(.node_class == $class and (.message | contains($text)))' >/dev/null
}

wait_log_contains_daemon() {
	local daemon="$1"
	local class="$2"
	local text="$3"
	retry "$ALMAGEST_E2E_TIMEOUT" "daemon $daemon log entry from $class containing $text" \
		log_contains_daemon "$daemon" "$class" "$text"
}

client_result_snapshot() {
	local node="$1"
	cli_daemon c graph menu --node "$node" >"$client_menu"
}

client_status() {
	jq -er '
		.blocks[]
		| select(.id == "result")
		| .items[]
		| select(.id == "status")
		| .text
	' "$client_menu"
}

client_field() {
	local field="$1"
	jq -er --arg field "$field" '
		.blocks[]
		| select(.id == "result")
		| .items[]
		| select(.id == $field)
		| (.value // "")
	' "$client_menu"
}

send_client_request() {
	local node="$1"
	cli_daemon c graph click-menu-button --node "$node" --block request --button send \
		>/tmp/e2e-wireguard-amnezia-click.json
}

assert_client_request_works() {
	local node="$1"
	local description="$2"
	local deadline=$((SECONDS + 90))
	local request_deadline
	local status=""
	local err=""
	local response=""

	while ((SECONDS <= deadline)); do
		client_result_snapshot "$node"
		status="$(client_status)"
		if [[ "$status" != "Status: running..." ]]; then
			send_client_request "$node"
		fi

		request_deadline=$((SECONDS + 30))
		while ((SECONDS <= request_deadline)); do
			client_result_snapshot "$node"
			status="$(client_status)"
			err="$(client_field error)"
			response="$(client_field response)"
			if [[ "$status" == "Status: 200 OK" && -z "$err" && "$response" == "Debug http server response" ]]; then
				return 0
			fi
			if [[ "$status" != "Status: running..." ]]; then
				break
			fi
			sleep 0.25
		done

		sleep 0.5
	done

	fail "$description did not receive tunneled response; status=$status error=$err response=$response"
}

tcpdump -n -U -i lo -w "$capture_file" \
	'(udp port 51830 or udp port 51831)' \
	>"$E2E_RUNTIME/tcpdump.log" 2>&1 &
capture_pid="$!"
sleep 0.5
kill -0 "$capture_pid" 2>/dev/null || fail "tcpdump did not start"

for name in "${daemon_names[@]}"; do
	start_daemon_named "$name"
done

wait_graph_node_daemon a "WireGuard A" || fail "daemon a WireGuard node missing"
wait_graph_node_daemon a "VTun A" || fail "daemon a VTun node missing"
wait_graph_node_daemon a "HTTP Server A" || fail "daemon a HTTP server node missing"
wait_graph_node_daemon a "OutNetwork A" || fail "daemon a OutNetwork node missing"

wait_graph_node_daemon b "WireGuard B" || fail "daemon b WireGuard node missing"
wait_graph_node_daemon b "VTun B" || fail "daemon b VTun node missing"
wait_graph_node_daemon b "HTTP Server B" || fail "daemon b HTTP server node missing"
wait_graph_node_daemon b "OutNetwork B" || fail "daemon b OutNetwork node missing"

wait_graph_node_daemon c "WireGuard C" || fail "daemon c WireGuard node missing"
wait_graph_node_daemon c "VTun C" || fail "daemon c VTun node missing"
wait_graph_node_daemon c "HTTP Client C to A" || fail "daemon c HTTP client to A node missing"
wait_graph_node_daemon c "HTTP Client C to B" || fail "daemon c HTTP client to B node missing"
wait_graph_node_daemon c "OutNetwork C" || fail "daemon c OutNetwork node missing"

wg_a="$(node_id_by_name_daemon a "WireGuard A")"
vtun_a="$(node_id_by_name_daemon a "VTun A")"
wg_b="$(node_id_by_name_daemon b "WireGuard B")"
vtun_b="$(node_id_by_name_daemon b "VTun B")"
wg_c="$(node_id_by_name_daemon c "WireGuard C")"
vtun_c="$(node_id_by_name_daemon c "VTun C")"
http_client_c_to_a="$(node_id_by_name_daemon c "HTTP Client C to A")"
http_client_c_to_b="$(node_id_by_name_daemon c "HTTP Client C to B")"

wait_log_contains_daemon a "almagest/HttpServer" "listening on 10.201.10.1:18080" \
	|| fail "daemon a HTTP server did not start listening"
wait_log_contains_daemon b "almagest/HttpServer" "listening on 10.201.20.1:18080" \
	|| fail "daemon b HTTP server did not start listening"

wait_node_label_daemon a "$wg_a" up || fail "daemon a WireGuard node did not become up"
wait_node_label_daemon b "$wg_b" up || fail "daemon b WireGuard node did not become up"
wait_node_label_daemon c "$wg_c" up || fail "daemon c WireGuard node did not become up"
wait_node_label_daemon a "$vtun_a" wg-amnezia-a || fail "daemon a VTun did not become ready"
wait_node_label_daemon b "$vtun_b" wg-amnezia-b || fail "daemon b VTun did not become ready"
wait_node_label_daemon c "$vtun_c" wg-amnezia-c || fail "daemon c VTun did not become ready"

assert_client_request_works "$http_client_c_to_a" "daemon c HTTP client to daemon a"
wait_node_label_daemon c "$http_client_c_to_a" "200 OK" \
	|| fail "daemon c HTTP client to daemon a did not report 200 OK"
wait_log_contains_daemon a "almagest/HttpServer" "GET /" \
	|| fail "daemon a HTTP server did not log the WireGuard request"

assert_client_request_works "$http_client_c_to_b" "daemon c HTTP client to daemon b"
wait_node_label_daemon c "$http_client_c_to_b" "200 OK" \
	|| fail "daemon c HTTP client to daemon b did not report 200 OK"
wait_log_contains_daemon b "almagest/HttpServer" "GET /" \
	|| fail "daemon b HTTP server did not log the AmneziaWG request"

stop_capture
plain_report="$E2E_RUNTIME/wireguard-signatures.txt"
amnezia_report="$E2E_RUNTIME/amnezia-signatures.txt"
capture_report 51830 "$plain_report"
capture_report 51831 "$amnezia_report"

plain_packets="$(report_value "$plain_report" packet_count)"
plain_init="$(report_value "$plain_report" initiation_packets)"
plain_response="$(report_value "$plain_report" response_packets)"
plain_transport="$(report_value "$plain_report" transport_packets)"
amnezia_packets="$(report_value "$amnezia_report" packet_count)"
amnezia_signatures="$(report_value "$amnezia_report" wireguard_signature_packets)"

((plain_packets > 0 && plain_init > 0 && plain_response > 0 && plain_transport > 0)) \
	|| fail "plain peer capture does not contain all standard WireGuard signatures: $(cat "$plain_report")"
((amnezia_packets > 0 && amnezia_signatures == 0)) \
	|| fail "AmneziaWG capture exposed standard WireGuard signatures: $(cat "$amnezia_report")"

log "case passed"
