#!/usr/bin/env bash
set -euo pipefail

: "${ALMAGEST_E2E_TIMEOUT:=45}"
source /e2e/lib/common.sh

upstream_iface="wg0"
upstream_log="$E2E_RUNTIME/amneziawg-go.log"
upstream_pid=""
http_pid=""
client_menu="/tmp/e2e-wireguard-amneziawg-go-compat-menu.json"

almagest_public_hex="89801e6c83deaecaf30e79eeca7a64c27c4417224330f7e7afe59cc09903f806"
upstream_private_hex="30c5cc124595c24feae219285b64b71c25d41afb031c57a5971bfb030fc79455"

fail() {
	log "FAILED: $*"
	if [[ -f "$E2E_DAEMON_LOG" ]]; then
		log "daemon log follows"
		tail -200 "$E2E_DAEMON_LOG" >&2 || true
	fi
	if [[ -f "$upstream_log" ]]; then
		log "amneziawg-go log follows"
		tail -200 "$upstream_log" >&2 || true
	fi
	ip addr show dev "$upstream_iface" >&2 2>/dev/null || true
	ip route show dev "$upstream_iface" >&2 2>/dev/null || true
	exit 1
}

cleanup() {
	trap - EXIT
	stop_static_http_target "$http_pid"
	if [[ -n "${upstream_pid:-}" ]]; then
		kill "$upstream_pid" 2>/dev/null || true
		wait "$upstream_pid" 2>/dev/null || true
		upstream_pid=""
	fi
	ip link del "$upstream_iface" 2>/dev/null || true
	stop_daemon
}

uapi_set() {
	local payload="$1"
	local reply
	reply="$(
		printf '%s\n\n' "$payload" \
			| socat - "UNIX-CONNECT:/var/run/amneziawg/$upstream_iface.sock"
	)"
	printf '%s\n\n' "$reply" >>"$E2E_RUNTIME/amneziawg-go-uapi.log"
	if ! grep -q '^errno=0$' <<<"$reply"; then
		fail "amneziawg-go UAPI request failed: $reply"
	fi
}

start_amneziawg_go() {
	mkdir -p /var/run/amneziawg
	: >"$upstream_log"
	log "starting upstream amneziawg-go"
	WG_PROCESS_FOREGROUND=1 LOG_LEVEL=verbose amneziawg-go -f "$upstream_iface" \
		>"$upstream_log" 2>&1 &
	upstream_pid="$!"

	retry "$ALMAGEST_E2E_TIMEOUT" "amneziawg-go UAPI socket" \
		test -S "/var/run/amneziawg/$upstream_iface.sock" \
		|| fail "amneziawg-go UAPI socket was not created"
	retry "$ALMAGEST_E2E_TIMEOUT" "amneziawg-go interface" \
		ip link show dev "$upstream_iface" >/dev/null \
		|| fail "amneziawg-go interface was not created"

	ip addr replace 10.202.20.1/32 dev "$upstream_iface"
	ip link set dev "$upstream_iface" mtu 1420 up
	ip route replace 10.202.10.1/32 dev "$upstream_iface"

	uapi_set "$(cat <<EOF
set=1
private_key=$upstream_private_hex
listen_port=51851
jc=2
jmin=11
jmax=23
s1=13
s2=17
s3=19
s4=29
h1=1111-1113
h2=2222-2225
h3=3333-3333
h4=4444-4449
i1=<b 0xaa55><rc 3><rd 2><t>
i2=<r 5>
i3=<rd 4>
i4=<rc 6>
i5=<b 0x01020304>
replace_peers=true
public_key=$almagest_public_hex
protocol_version=1
replace_allowed_ips=true
allowed_ip=10.202.10.1/32
endpoint=127.0.0.1:51850
persistent_keepalive_interval=1
EOF
)"
}

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
		>/tmp/e2e-wireguard-amneziawg-go-compat-click.json
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
			if [[ "$status" == "Status: 200 OK" && -z "$err" && "$response" == "amneziawg-go compatibility" ]]; then
				return 0
			fi
			if [[ "$status" != "Status: running..." ]]; then
				break
			fi
			sleep 0.25
		done

		sleep 0.5
	done

	fail "HTTP client did not receive amneziawg-go tunneled response; status=$status error=$err response=$response"
}

trap cleanup EXIT
start_daemon
trap cleanup EXIT
start_amneziawg_go
http_pid="$(start_static_http_target 10.202.20.1 18082 "amneziawg-go compatibility" "$E2E_RUNTIME/upstream-http-access.log")"

wait_graph_node "WireGuard Almagest" || fail "WireGuard Almagest node missing"
wait_graph_node "VTun Almagest" || fail "VTun Almagest node missing"
wait_graph_node "HTTP Client Compat" || fail "HTTP Client Compat node missing"

wg_node="$(node_id_by_name "WireGuard Almagest")"
vtun_node="$(node_id_by_name "VTun Almagest")"
http_client="$(node_id_by_name "HTTP Client Compat")"

wait_node_label "$wg_node" up || fail "WireGuard Almagest node did not become up"
wait_node_label "$vtun_node" wg-amneziawg-go-compat || fail "VTun Almagest node did not become ready"
retry "$ALMAGEST_E2E_TIMEOUT" "upstream HTTP target" \
	curl -fsS --max-time 2 http://10.202.20.1:18082/ >/tmp/e2e-amneziawg-go-http-self-test \
	|| fail "upstream HTTP target did not answer locally"

assert_client_request_works "$http_client"
wait_node_label "$http_client" "200 OK" || fail "HTTP client node did not report 200 OK"
grep -F "GET / HTTP/1.1" "$E2E_RUNTIME/upstream-http-access.log" >/dev/null \
	|| fail "upstream HTTP server did not log the tunneled request"

log "case passed"
