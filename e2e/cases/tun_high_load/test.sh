#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

load_requests="${ALMAGEST_E2E_TUN_LOAD_REQUESTS:-4000}"
load_concurrency="${ALMAGEST_E2E_TUN_LOAD_CONCURRENCY:-32}"
load_pid=""

cleanup() {
	if [[ -n "$load_pid" ]] && kill -0 "$load_pid" 2>/dev/null; then
		kill "$load_pid" 2>/dev/null || true
		wait "$load_pid" 2>/dev/null || true
	fi
	stop_daemon
	if [[ -x "$E2E_RUNTIME/cleanup-tun-high-load.sh" ]]; then
		"$E2E_RUNTIME/cleanup-tun-high-load.sh"
	fi
}

positive_integer() {
	[[ "$1" =~ ^[1-9][0-9]*$ ]]
}

assert_load_settings() {
	positive_integer "$load_requests" \
		|| fail "ALMAGEST_E2E_TUN_LOAD_REQUESTS must be a positive integer"
	positive_integer "$load_concurrency" \
		|| fail "ALMAGEST_E2E_TUN_LOAD_CONCURRENCY must be a positive integer"
	((load_requests >= 4)) \
		|| fail "ALMAGEST_E2E_TUN_LOAD_REQUESTS must be at least 4"
}

wait_graph_ready() {
	local node
	for node in \
		"Load direct TUN" \
		"Load direct Spoofer" \
		"Load middleware TUN" \
		"Load config stripper" \
		"Load firewall" \
		"Load middleware Spoofer" \
		"Load split TUN" \
		"Load splitter" \
		"Load split Spoofer A" \
		"Load split Spoofer B"; do
		wait_graph_node "$node" || fail "$node is missing"
	done

	wait_node_label "$(node_id_by_name "Load direct TUN")" almload0 \
		|| fail "direct TUN did not become ready"
	wait_node_label "$(node_id_by_name "Load direct Spoofer")" forwarding \
		|| fail "direct Spoofer did not start forwarding"
	wait_node_label "$(node_id_by_name "Load middleware TUN")" almload1 \
		|| fail "middleware TUN did not become ready"
	wait_node_label "$(node_id_by_name "Load config stripper")" "stripping config" \
		|| fail "TunConfigStripper did not become ready"
	wait_node_label "$(node_id_by_name "Load firewall")" "filtering 0 rules" \
		|| fail "TunFirewall did not become ready"
	wait_node_label "$(node_id_by_name "Load middleware Spoofer")" forwarding \
		|| fail "middleware Spoofer did not start forwarding"
	wait_node_label "$(node_id_by_name "Load split TUN")" almload2 \
		|| fail "split TUN did not become ready"
	wait_node_label "$(node_id_by_name "Load splitter")" splitting \
		|| fail "TunSplitter did not become ready"
	wait_node_label "$(node_id_by_name "Load split Spoofer A")" forwarding \
		|| fail "split Spoofer A did not start forwarding"
	wait_node_label "$(node_id_by_name "Load split Spoofer B")" forwarding \
		|| fail "split Spoofer B did not start forwarding"

	wait_log_contains "almagest/HttpServer" "listening on 10.221.1.2:18201" \
		|| fail "direct load server did not start"
	wait_log_contains "almagest/HttpServer" "listening on 10.222.1.2:18202" \
		|| fail "middleware load server did not start"
	wait_log_contains "almagest/HttpServer" "listening on 10.223.1.2:18203" \
		|| fail "split load server A did not start"
	wait_log_contains "almagest/HttpServer" "listening on 10.223.1.2:18204" \
		|| fail "split load server B did not start"
}

run_load() {
	local scenario="$1"
	local ready_file="${2:-}"
	local result="$E2E_RUNTIME/load-$scenario.json"

	log "running $scenario TUN load: $load_requests requests, $load_concurrency workers"
	python3 - "$load_requests" "$load_concurrency" "$scenario" "$ready_file" >"$result" <<'PY'
import concurrent.futures
import json
import os
import socket
import sys
import threading
import time

request_count = int(sys.argv[1])
concurrency = int(sys.argv[2])
scenario = sys.argv[3]
ready_file = sys.argv[4]
targets = (
    ("10.221.1.2", 18201, b"direct-load-response"),
    ("10.222.1.2", 18202, b"middleware-load-response"),
    ("10.223.1.2", 18203, b"split-a-load-response"),
    ("10.223.1.2", 18204, b"split-b-load-response"),
)
start_gate = threading.Event()


def request(index):
    start_gate.wait()
    host, port, expected = targets[index % len(targets)]
    path = f"/{scenario}/{index}"
    with socket.create_connection((host, port), timeout=8) as conn:
        conn.settimeout(8)
        request_data = (
            f"GET {path} HTTP/1.1\r\n"
            f"Host: {host}\r\n"
            "Connection: close\r\n\r\n"
        ).encode()
        conn.sendall(request_data)
        response = bytearray()
        while True:
            chunk = conn.recv(16384)
            if not chunk:
                break
            response.extend(chunk)

    header, separator, body = bytes(response).partition(b"\r\n\r\n")
    status = header.split(b"\r\n", 1)[0]
    if not separator or b" 200 " not in status or body != expected:
        raise RuntimeError(
            f"request {index} to {host}:{port} returned "
            f"status={status!r}, body={body!r}"
        )
    return index % len(targets)


started = time.monotonic()
counts = [0] * len(targets)
failures = []
if not ready_file:
    start_gate.set()
with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as pool:
    futures = [pool.submit(request, index) for index in range(request_count)]
    if ready_file:
        with open(ready_file, "w", encoding="utf-8") as ready:
            ready.write(str(os.getpid()))
    start_gate.set()
    for future in concurrent.futures.as_completed(futures):
        try:
            counts[future.result()] += 1
        except Exception as error:
            failures.append(str(error))

summary = {
    "scenario": scenario,
    "requests": request_count,
    "concurrency": concurrency,
    "counts": counts,
    "failures": len(failures),
    "elapsed_seconds": round(time.monotonic() - started, 3),
}
print(json.dumps(summary, sort_keys=True))
if failures:
    for failure in failures[:20]:
        print(failure, file=sys.stderr)
    raise SystemExit(1)
PY

	jq -e \
		--argjson requests "$load_requests" \
		'.requests == $requests and .failures == 0 and (.counts | length) == 4 and (all(.counts[]; . > 0))' \
		"$result" >/dev/null \
		|| fail "$scenario load summary is invalid"
	log "$(<"$result")"
}

apply_live_updates() {
	local firewall
	local splitter
	firewall="$(node_id_by_name "Load firewall")"
	splitter="$(node_id_by_name "Load splitter")"

	# Use a non-matching rule to replace the live firewall filter without
	# changing the expected traffic. Then restore the empty policy.
	jq -n '{config:{rules:"{\"exclude\":[{\"network\":\"udp\",\"ports\":[9]}]}"}}' \
		| cli graph apply-menu --node "$firewall" \
		>"$E2E_RUNTIME/firewall-load-rule.json"
	wait_node_label "$firewall" "filtering 1 rules" \
		|| fail "TunFirewall did not apply the live load rule"
	jq -n '{config:{rules:"{}"}}' \
		| cli graph apply-menu --node "$firewall" \
		>"$E2E_RUNTIME/firewall-load-clear.json"
	wait_node_label "$firewall" "filtering 0 rules" \
		|| fail "TunFirewall did not clear the live load rule"

	# Recompile equivalent splitter rules in a different order. Both output
	# frontends must stay attached to their original services.
	jq -n --arg rules $'PORT 18204\nSLOT 2\n\nPORT 18203\nSLOT 1' \
		'{config:{rules:$rules}}' \
		| cli graph apply-menu --node "$splitter" \
		>"$E2E_RUNTIME/splitter-load-rules.json"
	wait_node_label "$splitter" splitting \
		|| fail "TunSplitter did not recover after its live rule update"
}

assert_load_settings
trap cleanup EXIT

start_daemon
wait_graph_ready
run_load initial

load_ready="$E2E_RUNTIME/reconfigured-load.ready"
rm -f "$load_ready"
run_load reconfigured "$load_ready" &
load_pid="$!"
retry "$ALMAGEST_E2E_TIMEOUT" "reconfigured load to start" test -s "$load_ready" \
	|| fail "reconfigured load did not start"
apply_live_updates
if ! wait "$load_pid"; then
	load_pid=""
	fail "reconfigured load failed during live graph updates"
fi
load_pid=""

stop_daemon
start_daemon
wait_graph_ready
run_load restarted

log "case passed"
