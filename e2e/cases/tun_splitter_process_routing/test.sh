#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

public_ns="alm-e2e-ts-public"
public_host="203.0.115.2"
public_port="18096"
public_body="TunSplitter OutNetwork public response"
public_log="/tmp/e2e-tun-splitter-public.log"
public_pid=""

cleanup() {
	stop_static_http_target "$public_pid"
	stop_daemon
	if [[ -x "$E2E_RUNTIME/cleanup-netns.sh" ]]; then
		"$E2E_RUNTIME/cleanup-netns.sh"
	fi
}

curl_split_target() {
	local output="$1"
	curl --fail --silent --show-error --noproxy '*' \
		--connect-timeout 2 --max-time 5 \
		http://10.210.148.1:8080/ \
		--output "$output" \
		2>/tmp/e2e-tun-splitter-curl.err
}

wget_split_target() {
	local output="$1"
	timeout 6 wget --quiet --no-proxy --tries=1 --timeout=3 \
		--output-document="$output" \
		http://10.210.148.1:8080/ \
		2>/tmp/e2e-tun-splitter-wget.err
}

python_http_target() {
	local path="$1"
	local output="$2"
	python3 - "$public_host" "$public_port" "$path" "$output" 2>/tmp/e2e-tun-splitter-python-http.err <<'PY'
import socket
import sys

host = sys.argv[1]
port = int(sys.argv[2])
path = sys.argv[3]
output = sys.argv[4]

with socket.create_connection((host, port), timeout=4) as conn:
    conn.settimeout(4)
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

assert_body() {
	local file="$1"
	local want="$2"
	local got
	got="$(cat "$file")"
	[[ "$got" == "$want" ]] || fail "unexpected response body: $got"
}

public_pid="$(start_static_http_target_in_netns "$public_ns" "$public_host" "$public_port" "$public_body" "$public_log")"
trap cleanup EXIT

retry "$ALMAGEST_E2E_TIMEOUT" "baseline public target before daemon" \
	python_http_target /baseline /tmp/e2e-tun-splitter-public-baseline.txt \
	|| fail "public target was not reachable before daemon startup"
assert_body /tmp/e2e-tun-splitter-public-baseline.txt "$public_body"

start_daemon

wait_node_class_available "almagest/DefaultTun" \
	|| fail "DefaultTun node class is unavailable"
wait_node_class_available "almagest/TunSplitter" \
	|| fail "TunSplitter node class is unavailable"
wait_node_class_available "almagest/TunCopy" \
	|| fail "TunCopy node class is unavailable"
wait_node_class_available "almagest/VTun" \
	|| fail "VTun node class is unavailable"
wait_node_class_available "almagest/Spoofer" \
	|| fail "Spoofer node class is unavailable"
wait_node_class_available "almagest/OutNetwork" \
	|| fail "OutNetwork node class is unavailable"
wait_node_class_available "almagest/MockNetwork" \
	|| fail "MockNetwork node class is unavailable"
wait_node_class_available "almagest/HttpServer" \
	|| fail "HttpServer node class is unavailable"

wait_graph_node "DefaultTun 1501" || fail "DefaultTun node missing"
wait_graph_node "TunSplitter 1502" || fail "TunSplitter node missing"
wait_graph_node "TunCopy 1505" || fail "TunCopy node missing"
wait_graph_node "VTun 1506" || fail "VTun node missing"
wait_graph_node "Spoofer 1503" || fail "curl Spoofer node missing"
wait_graph_node "Spoofer 1508" || fail "fallback Spoofer node missing"

default_tun="$(node_id_by_name "DefaultTun 1501")"
splitter="$(node_id_by_name "TunSplitter 1502")"
copy="$(node_id_by_name "TunCopy 1505")"
curl_spoofer="$(node_id_by_name "Spoofer 1503")"
out_spoofer="$(node_id_by_name "Spoofer 1508")"

wait_node_label "$default_tun" ready || fail "DefaultTun did not become ready"
wait_node_label "$splitter" splitting || fail "TunSplitter did not start splitting"
wait_node_label "$copy" copying || fail "TunCopy did not start copying"
wait_node_label "$curl_spoofer" forwarding || fail "curl Spoofer did not start forwarding"
wait_node_label "$out_spoofer" forwarding || fail "fallback Spoofer did not start forwarding"

wait_log_contains "almagest/HttpServer" "listening on 10.210.148.1:8080" \
	|| fail "internal HTTP servers did not start listening"

retry "$ALMAGEST_E2E_TIMEOUT" "curl routed to Server 1 slot" \
	curl_split_target /tmp/e2e-tun-splitter-curl.txt \
	|| fail "curl request to split target failed"
assert_body /tmp/e2e-tun-splitter-curl.txt "Server 1"

retry "$ALMAGEST_E2E_TIMEOUT" "wget routed to Server 2 slot" \
	wget_split_target /tmp/e2e-tun-splitter-wget.txt \
	|| fail "wget request to split target failed"
assert_body /tmp/e2e-tun-splitter-wget.txt "Server 2"

retry "$ALMAGEST_E2E_TIMEOUT" "non curl/wget traffic through OutNetwork fallback" \
	python_http_target /outnetwork-fallback /tmp/e2e-tun-splitter-public-through-outnetwork.txt \
	|| fail "fallback traffic did not reach public target through OutNetwork"
assert_body /tmp/e2e-tun-splitter-public-through-outnetwork.txt "$public_body"

stop_daemon

retry "$ALMAGEST_E2E_TIMEOUT" "public target after daemon stop" \
	python_http_target /after-daemon-stop /tmp/e2e-tun-splitter-public-stopped.txt \
	|| fail "public target was not reachable after daemon shutdown"
assert_body /tmp/e2e-tun-splitter-public-stopped.txt "$public_body"

grep -F "GET /baseline" "$public_log" >/dev/null \
	|| fail "public target did not receive baseline request"
grep -F "GET /outnetwork-fallback" "$public_log" >/dev/null \
	|| fail "public target did not receive fallback request"
grep -F "GET /after-daemon-stop" "$public_log" >/dev/null \
	|| fail "public target did not receive post-daemon request"

log "case passed"
