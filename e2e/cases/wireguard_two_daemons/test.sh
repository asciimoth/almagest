#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

daemon_pid_a=""
daemon_pid_b=""
admin_sock_a="$E2E_RUNTIME/daemon-a.sock"
admin_sock_b="$E2E_RUNTIME/daemon-b.sock"
daemon_log_a="$E2E_RUNTIME/daemon-a.log"
daemon_log_b="$E2E_RUNTIME/daemon-b.log"
client_menu="/tmp/e2e-wireguard-two-daemons-client-menu.json"

fail() {
	log "FAILED: $*"
	for daemon_log in "$daemon_log_a" "$daemon_log_b"; do
		if [[ -f "$daemon_log" ]]; then
			log "$(basename "$daemon_log") follows"
			tail -200 "$daemon_log" >&2 || true
		fi
	done
	exit 1
}

cli_a() {
	almagest-cli --url "unix://$admin_sock_a" "$@"
}

cli_b() {
	almagest-cli --url "unix://$admin_sock_b" "$@"
}

stop_daemons() {
	trap - EXIT
	for pid_var in daemon_pid_a daemon_pid_b; do
		local pid="${!pid_var:-}"
		if [[ -z "$pid" ]]; then
			continue
		fi
		if kill -0 "$pid" 2>/dev/null; then
			log "stopping daemon ${pid_var#daemon_pid_}"
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
		printf -v "$pid_var" ''
	done
}

start_daemon_a() {
	mkdir -p "$E2E_RUNTIME"
	: >"$daemon_log_a"
	log "starting daemon a"
	almagestd \
		-c "$E2E_CONFIG_DIR/daemon-a" \
		-l debug \
		--debug-nodes \
		--unsafe-config \
		--unix "$admin_sock_a" \
		--web 0 \
		>"$daemon_log_a" 2>&1 &
	daemon_pid_a="$!"
	trap stop_daemons EXIT
	retry "$ALMAGEST_E2E_TIMEOUT" "daemon a admin API socket" test -S "$admin_sock_a" \
		|| fail "daemon a admin API socket was not created"
	retry "$ALMAGEST_E2E_TIMEOUT" "daemon a admin API ping" \
		cli_a admin ping --nonce "$E2E_CASE-a" >/tmp/e2e-wireguard-two-daemons-ping-a.json \
		|| fail "daemon a admin API did not answer ping"
	jq -e --arg nonce "$E2E_CASE-a" '.nonce == $nonce' /tmp/e2e-wireguard-two-daemons-ping-a.json >/dev/null \
		|| fail "daemon a admin API ping returned unexpected payload"
}

start_daemon_b() {
	mkdir -p "$E2E_RUNTIME"
	: >"$daemon_log_b"
	log "starting daemon b"
	almagestd \
		-c "$E2E_CONFIG_DIR/daemon-b" \
		-l debug \
		--debug-nodes \
		--unsafe-config \
		--unix "$admin_sock_b" \
		--web 0 \
		>"$daemon_log_b" 2>&1 &
	daemon_pid_b="$!"
	trap stop_daemons EXIT
	retry "$ALMAGEST_E2E_TIMEOUT" "daemon b admin API socket" test -S "$admin_sock_b" \
		|| fail "daemon b admin API socket was not created"
	retry "$ALMAGEST_E2E_TIMEOUT" "daemon b admin API ping" \
		cli_b admin ping --nonce "$E2E_CASE-b" >/tmp/e2e-wireguard-two-daemons-ping-b.json \
		|| fail "daemon b admin API did not answer ping"
	jq -e --arg nonce "$E2E_CASE-b" '.nonce == $nonce' /tmp/e2e-wireguard-two-daemons-ping-b.json >/dev/null \
		|| fail "daemon b admin API ping returned unexpected payload"
}

node_id_by_name_a() {
	local name="$1"
	cli_a graph map | jq -er --arg name "$name" '.nodes[$name].ID'
}

node_id_by_name_b() {
	local name="$1"
	cli_b graph map | jq -er --arg name "$name" '.nodes[$name].ID'
}

wait_graph_node_a() {
	local name="$1"
	retry "$ALMAGEST_E2E_TIMEOUT" "daemon a graph node $name" \
		node_id_by_name_a "$name" >/dev/null
}

wait_graph_node_b() {
	local name="$1"
	retry "$ALMAGEST_E2E_TIMEOUT" "daemon b graph node $name" \
		node_id_by_name_b "$name" >/dev/null
}

node_label_a() {
	local id="$1"
	cli_a graph node --id "$id" | jq -er '.snapshot.label'
}

node_label_b() {
	local id="$1"
	cli_b graph node --id "$id" | jq -er '.snapshot.label'
}

node_label_is_a() {
	local id="$1"
	local label="$2"
	[[ "$(node_label_a "$id")" == "$label" ]]
}

node_label_is_b() {
	local id="$1"
	local label="$2"
	[[ "$(node_label_b "$id")" == "$label" ]]
}

wait_node_label_a() {
	local id="$1"
	local label="$2"
	retry "$ALMAGEST_E2E_TIMEOUT" "daemon a node $id label $label" \
		node_label_is_a "$id" "$label"
}

wait_node_label_b() {
	local id="$1"
	local label="$2"
	retry "$ALMAGEST_E2E_TIMEOUT" "daemon b node $id label $label" \
		node_label_is_b "$id" "$label"
}

log_contains_b() {
	local class="$1"
	local text="$2"
	cli_b log subscribe | jq -e --arg class "$class" --arg text "$text" \
		'.entries[] | select(.node_class == $class and (.message | contains($text)))' >/dev/null
}

wait_log_contains_b() {
	local class="$1"
	local text="$2"
	retry "$ALMAGEST_E2E_TIMEOUT" "daemon b log entry from $class containing $text" \
		log_contains_b "$class" "$text"
}

client_result_snapshot() {
	local node="$1"
	cli_a graph menu --node "$node" >"$client_menu"
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
	cli_a graph click-menu-button --node "$node" --block request --button send \
		>/tmp/e2e-wireguard-two-daemons-click.json
}

assert_client_request_works() {
	local node="$1"
	local deadline=$((SECONDS + 45))
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

		request_deadline=$((SECONDS + 20))
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

		sleep 0.25
	done

	fail "HTTP client did not receive tunneled response; status=$status error=$err response=$response"
}

start_daemon_a
start_daemon_b

wait_graph_node_a "WireGuard A" || fail "daemon a WireGuard node missing"
wait_graph_node_a "VTun A" || fail "daemon a VTun node missing"
wait_graph_node_a "HTTP Client A" || fail "daemon a HTTP client node missing"
wait_graph_node_a "OutNetwork A" || fail "daemon a OutNetwork node missing"

wait_graph_node_b "WireGuard B" || fail "daemon b WireGuard node missing"
wait_graph_node_b "VTun B" || fail "daemon b VTun node missing"
wait_graph_node_b "HTTP Server B" || fail "daemon b HTTP server node missing"
wait_graph_node_b "OutNetwork B" || fail "daemon b OutNetwork node missing"

wg_a="$(node_id_by_name_a "WireGuard A")"
vtun_a="$(node_id_by_name_a "VTun A")"
http_client_a="$(node_id_by_name_a "HTTP Client A")"
wg_b="$(node_id_by_name_b "WireGuard B")"
vtun_b="$(node_id_by_name_b "VTun B")"

wait_log_contains_b "almagest/HttpServer" "listening on 10.198.20.1:18080" \
	|| fail "daemon b HTTP server did not start listening"

wait_node_label_a "$wg_a" up || fail "daemon a WireGuard node did not become up"
wait_node_label_b "$wg_b" up || fail "daemon b WireGuard node did not become up"
wait_node_label_a "$vtun_a" wg-two-daemons-a || fail "daemon a VTun did not become ready"
wait_node_label_b "$vtun_b" wg-two-daemons-b || fail "daemon b VTun did not become ready"

assert_client_request_works "$http_client_a"
wait_node_label_a "$http_client_a" "200 OK" || fail "daemon a HTTP client node did not report 200 OK"

wait_log_contains_b "almagest/HttpServer" "GET /" \
	|| fail "daemon b HTTP server did not log the WireGuard request"

log "case passed"
