#!/usr/bin/env bash
set -euo pipefail
# shellcheck disable=SC1091 # The helper is supplied by the E2E image.
source /e2e/lib/common.sh
upstream_pid=""
http_pid=""
upstream_log="$E2E_RUNTIME/yggdrasil-go.log"
upstream_config="$E2E_RUNTIME/yggdrasil-go.json"
upstream_socket="$E2E_RUNTIME/yggdrasil-go.sock"
client_menu="$E2E_RUNTIME/client-menu.json"
fail() {
 log "FAILED: $*"
 tail -100 "$upstream_log" "$E2E_DAEMON_LOG" >&2 || true
 exit 1
}
cleanup() {
 trap - EXIT
 stop_static_http_target "$http_pid"
 if [[ -n "$upstream_pid" ]]; then
  kill "$upstream_pid" 2>/dev/null || true
  wait "$upstream_pid" 2>/dev/null || true
 fi
 stop_daemon
}
reference_ready() { yggdrasilctl -endpoint="unix://$upstream_socket" -json getSelf >"$E2E_RUNTIME/reference-self.json"; }
client_result_snapshot() {
	local node="$1"
	cli graph menu --node "$node" >"$client_menu"
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
	cli graph click-menu-button --node "$node" --block request --button send \
		>/tmp/e2e-wireguard-yggdrasil-go-compat-click.json
}

assert_client_request_works() {
	local node="$1"
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
			if [[ "$status" == "Status: 200 OK" && -z "$err" && "$response" == "yggdrasil-go compatibility" ]]; then
				return 0
			fi
			if [[ "$status" != "Status: running..." ]]; then
				break
			fi
			sleep 0.25
		done

		sleep 0.5
	done

	fail "HTTP client did not receive yggdrasil-go tunneled response; status=$status error=$err response=$response"
}


trap cleanup EXIT
yggdrasil -genconf -json | jq --arg socket "unix://$upstream_socket" \
 '.AdminListen=$socket | .IfName="ygg-compat-ref" | .Listen=["tcp://127.0.0.1:19002"] | .Peers=[] | .MulticastInterfaces=[]' >"$upstream_config"
yggdrasil -useconffile "$upstream_config" >"$upstream_log" 2>&1 &
upstream_pid="$!"
retry "$ALMAGEST_E2E_TIMEOUT" "reference node" reference_ready || fail "reference did not start"
reference_address="$(jq -er '.self.address // .address' "$E2E_RUNTIME/reference-self.json")"
http_pid="$(start_static_http_target "$reference_address" 18082 "yggdrasil-go compatibility" "$E2E_RUNTIME/http.log")"
jq --arg url "http://[$reference_address]:18082/" '.nodes["HTTP Client"].url=$url' \
 "$E2E_CONFIG_DIR/alm.hujson" >"$E2E_RUNTIME/alm.updated.json"
cp "$E2E_RUNTIME/alm.updated.json" "$E2E_CONFIG_DIR/alm.hujson"
start_daemon
trap cleanup EXIT
wait_graph_node "Yggdrasil" || fail "node missing"
ygg_node="$(node_id_by_name "Yggdrasil")"
http_client="$(node_id_by_name "HTTP Client")"
wait_node_label "$ygg_node" up || fail "node did not become ready"
# Set the request URL through the saved config before starting the daemon.
assert_client_request_works "$http_client"
# Restart the independent reference process and prove persistent peer recovery.
kill "$upstream_pid"
wait "$upstream_pid" || true
yggdrasil -useconffile "$upstream_config" >>"$upstream_log" 2>&1 &
upstream_pid="$!"
retry "$ALMAGEST_E2E_TIMEOUT" "restarted reference" reference_ready || fail "reference restart failed"
assert_client_request_works "$http_client"
log "case passed"
