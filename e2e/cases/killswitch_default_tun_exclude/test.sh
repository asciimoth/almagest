#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

target_ns="alm-e2e-ks-target"
target_host="203.0.114.2"
target_port="18095"
target="$target_host:$target_port"
target_body="killswitch DefaultTun target response"
target_log="/tmp/e2e-killswitch-default-tun-target.log"
target_pid=""
killswitch_pid=""
killswitch_sock="$E2E_RUNTIME/killswitch.sock"

cleanup() {
	stop_static_http_target "$target_pid"
	stop_daemon
	stop_killswitch
	if [[ -x "$E2E_RUNTIME/cleanup-netns.sh" ]]; then
		"$E2E_RUNTIME/cleanup-netns.sh"
	fi
}

curl_target() {
	local path="$1"
	local output="$2"
	curl --fail --silent --show-error --connect-timeout 1 --max-time 3 \
		"http://$target$path" \
		--output "$output"
}

curl_target_fails() {
	local path="$1"
	if curl_target "$path" /tmp/e2e-killswitch-unexpected-curl-body.txt \
		>/tmp/e2e-killswitch-unexpected-curl-stdout.txt \
		2>/tmp/e2e-killswitch-expected-curl-failure.txt; then
		return 1
	fi
	return 0
}

python_http_target() {
	local path="$1"
	local output="$2"
	python3 - "$target_host" "$target_port" "$path" "$output" <<'PY'
import socket
import sys

host = sys.argv[1]
port = int(sys.argv[2])
path = sys.argv[3]
output = sys.argv[4]

with socket.create_connection((host, port), timeout=3) as conn:
    conn.settimeout(3)
    conn.sendall(
        f"GET {path} HTTP/1.1\r\nHost: {host}\r\nConnection: close\r\n\r\n".encode()
    )
    data = bytearray()
    while True:
        chunk = conn.recv(4096)
        if not chunk:
            break
        data.extend(chunk)

header, sep, body = bytes(data).partition(b"\r\n\r\n")
if not sep or b" 200 " not in header.split(b"\r\n", 1)[0]:
    raise SystemExit(f"bad HTTP response: {bytes(data)!r}")
with open(output, "wb") as out:
    out.write(body)
PY
}

python_http_target_fails() {
	local path="$1"
	if python_http_target "$path" /tmp/e2e-killswitch-unexpected-python-body.txt \
		>/tmp/e2e-killswitch-unexpected-python-stdout.txt \
		2>/tmp/e2e-killswitch-expected-python-failure.txt; then
		return 1
	fi
	return 0
}

assert_body() {
	local file="$1"
	local want="$2"
	local got
	got="$(cat "$file")"
	[[ "$got" == "$want" ]] || fail "unexpected response body: $got"
}

start_killswitch() {
	if [[ -n "$killswitch_pid" ]] && kill -0 "$killswitch_pid" 2>/dev/null; then
		fail "killswitch is already running"
	fi
	: >"$E2E_RUNTIME/killswitch.log"
	log "starting killswitch"
	killswitch "$E2E_RUNTIME/killswitch.json" >"$E2E_RUNTIME/killswitch.log" 2>&1 &
	killswitch_pid="$!"
	retry "$ALMAGEST_E2E_TIMEOUT" "killswitch admin API socket" test -S "$killswitch_sock" \
		|| fail_killswitch "killswitch admin API socket was not created"
	retry "$ALMAGEST_E2E_TIMEOUT" "killswitch managed almks0 interface" \
		killswitch_has_attached_interface \
		|| fail_killswitch "killswitch did not attach to almks0"
}

stop_killswitch() {
	if [[ -z "${killswitch_pid:-}" ]]; then
		return 0
	fi
	if kill -0 "$killswitch_pid" 2>/dev/null; then
		log "stopping killswitch"
		kill "$killswitch_pid" 2>/dev/null || true
		local deadline=$((SECONDS + 10))
		while kill -0 "$killswitch_pid" 2>/dev/null && ((SECONDS <= deadline)); do
			sleep 0.25
		done
		if kill -0 "$killswitch_pid" 2>/dev/null; then
			kill -KILL "$killswitch_pid" 2>/dev/null || true
		fi
	fi
	wait "$killswitch_pid" 2>/dev/null || true
	killswitch_pid=""
}

fail_killswitch() {
	log "FAILED: $*"
	if [[ -f "$E2E_RUNTIME/killswitch.log" ]]; then
		log "killswitch log follows"
		tail -200 "$E2E_RUNTIME/killswitch.log" >&2 || true
	fi
	fail "$*"
}

killswitch_cfg() {
	killswitch-cli get-cfg -s "$killswitch_sock" --json-out
}

killswitch_has_attached_interface() {
	killswitch_cfg | jq -e '.effective_interfaces[]? | select(.name == "almks0" and .attached == true)' >/dev/null
}

killswitch_tmp_mark_allowed() {
	local mark="$1"
	killswitch_cfg | jq -e --arg mark "$mark" '
		.tmp_rulesets[]?
		| .policy.allowed_marks[]?
		| select(ascii_downcase == ($mark | ascii_downcase))
	' >/dev/null
}

target_pid="$(start_static_http_target_in_netns "$target_ns" "$target_host" "$target_port" "$target_body" "$target_log")"
trap cleanup EXIT

retry "$ALMAGEST_E2E_TIMEOUT" "baseline target before killswitch" \
	curl_target /baseline /tmp/e2e-killswitch-baseline.txt \
	|| fail "target was not reachable before killswitch startup"
assert_body /tmp/e2e-killswitch-baseline.txt "$target_body"

start_killswitch

retry "$ALMAGEST_E2E_TIMEOUT" "killswitch blocks unmarked curl" \
	curl_target_fails /blocked-by-killswitch \
	|| fail "killswitch did not block unmarked curl traffic"

start_daemon

wait_node_class_available "almagest/DefaultTun" \
	|| fail "DefaultTun node class is unavailable"
wait_node_class_available "almagest/Spoofer" \
	|| fail "Spoofer node class is unavailable"
wait_node_class_available "almagest/OutNetwork" \
	|| fail "OutNetwork node class is unavailable"
wait_node_class_available "almagest/LogNetwork" \
	|| fail "debug network log node class is unavailable"
wait_graph_node "DefaultTun 1401" || fail "DefaultTun node missing"
default_tun="$(node_id_by_name "DefaultTun 1401")"
wait_node_label "$default_tun" ready || fail "DefaultTun did not become ready"

retry "$ALMAGEST_E2E_TIMEOUT" "killswitch allows almagestd app mark" \
	killswitch_tmp_mark_allowed "0xeb9f0001" \
	|| fail_killswitch "almagestd app mark was not allowed in killswitch"
retry "$ALMAGEST_E2E_TIMEOUT" "killswitch allows DefaultTun exclude mark" \
	killswitch_tmp_mark_allowed "0x414c4d01" \
	|| fail_killswitch "DefaultTun exclude mark was not allowed in killswitch"

retry "$ALMAGEST_E2E_TIMEOUT" "excluded curl direct route through killswitch" \
	curl_target /excluded-direct /tmp/e2e-killswitch-excluded-direct.txt \
	|| fail "DefaultTun exclude rule did not allow curl direct traffic"
assert_body /tmp/e2e-killswitch-excluded-direct.txt "$target_body"

retry "$ALMAGEST_E2E_TIMEOUT" "unexcluded python blocked by disconnected DefaultTun" \
	python_http_target_fails /python-before-outnetwork \
	|| fail "unexcluded Python traffic unexpectedly bypassed disconnected DefaultTun"

spoofer="$(add_node "almagest/Spoofer" "Spoofer 1402")"
log_network="$(add_node "almagest/LogNetwork" "LogNetwork 1403")"
out_network="$(add_node "almagest/OutNetwork" "OutNetwork 1404")"

add_link_by_port_name "$default_tun" right Tun "$spoofer" left Tun >/tmp/e2e-killswitch-link-tun.json
add_link_by_port_name "$spoofer" right Network "$log_network" left Network >/tmp/e2e-killswitch-link-log.json
add_link_by_port_name "$log_network" right Network "$out_network" left Network >/tmp/e2e-killswitch-link-out.json

wait_node_label "$spoofer" forwarding || fail "Spoofer did not start forwarding"

retry "$ALMAGEST_E2E_TIMEOUT" "unexcluded python through DefaultTun OutNetwork chain" \
	python_http_target /python-through-outnetwork /tmp/e2e-killswitch-python-chain.txt \
	|| fail "unexcluded Python traffic did not work through DefaultTun/Spoofer/OutNetwork"
assert_body /tmp/e2e-killswitch-python-chain.txt "$target_body"

wait_log_contains "almagest/LogNetwork" "Dial tcp $target" \
	|| fail "debug network log did not record target dial"
wait_log_contains "almagest/LogNetwork" "GET /python-through-outnetwork" \
	|| fail "debug network log did not record proxied Python request"
wait_log_contains "almagest/LogNetwork" "$target_body" \
	|| fail "debug network log did not record proxied Python response"

stop_daemon

retry "$ALMAGEST_E2E_TIMEOUT" "killswitch blocks curl after almagestd stop" \
	curl_target_fails /blocked-after-daemon-stop \
	|| fail "killswitch did not block curl after almagestd removed temporary allow rules"

grep -F "GET /baseline" "$target_log" >/dev/null \
	|| fail "target did not receive baseline request"
grep -F "GET /excluded-direct" "$target_log" >/dev/null \
	|| fail "target did not receive excluded direct request"
grep -F "GET /python-through-outnetwork" "$target_log" >/dev/null \
	|| fail "target did not receive proxied Python request"
if grep -F "GET /blocked-after-daemon-stop" "$target_log" >/dev/null; then
	fail "target received request after daemon stopped"
fi

log "case passed"
