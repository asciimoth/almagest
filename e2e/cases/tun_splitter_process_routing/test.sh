#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

public_ns="alm-e2e-ts-public"
public_host="203.0.115.2"
public_port="18096"
public_body="TunSplitter OutNetwork public response"
public_log="/tmp/e2e-tun-splitter-public.log"
public_pid=""
pid_client_pid=""

cleanup() {
	stop_static_http_target "$pid_client_pid"
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

exec_split_target() {
	local output="$1"
	/tmp/e2e-split-exec-client --fail --silent --show-error --noproxy '*' \
		--connect-timeout 2 --max-time 5 \
		http://10.210.148.1:8080/ \
		--output "$output" \
		2>/tmp/e2e-tun-splitter-exec.err
}

python_split_target() {
	local output="$1"
	python3 - 10.210.148.1 8080 / "$output" 2>/tmp/e2e-tun-splitter-python-split.err <<'PY'
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

python_split_target_as_uid() {
	local uid="$1"
	local output="$2"
	run_as_uid "$uid" "$uid" python3 - 10.210.148.1 8080 / "$output" 2>/tmp/e2e-tun-splitter-python-uid.err <<'PY'
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

start_pid_split_client() {
	local ready="$1"
	local trigger="$2"
	local output="$3"
	local err="$4"
	rm -f "$ready" "$trigger" "$output" "$err"
	python3 - "$ready" "$trigger" "$output" 2>"$err" <<'PY' &
import os
import socket
import sys
import time

ready = sys.argv[1]
trigger = sys.argv[2]
output = sys.argv[3]

with open(ready, "w", encoding="utf-8") as out:
    out.write(str(os.getpid()))

deadline = time.monotonic() + 20
while not os.path.exists(trigger):
    if time.monotonic() > deadline:
        raise SystemExit("timed out waiting for trigger")
    time.sleep(0.05)

with socket.create_connection(("10.210.148.1", 8080), timeout=4) as conn:
    conn.settimeout(4)
    conn.sendall(b"GET /pid HTTP/1.1\r\nHost: 10.210.148.1\r\nConnection: close\r\n\r\n")
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
	pid_client_pid="$!"
}

wait_process() {
	local pid="$1"
	local deadline=$((SECONDS + ALMAGEST_E2E_TIMEOUT))
	while kill -0 "$pid" 2>/dev/null && ((SECONDS <= deadline)); do
		sleep 0.25
	done
	if kill -0 "$pid" 2>/dev/null; then
		return 1
	fi
	wait "$pid"
}

splitter_rules_with_pid() {
	local pid="$1"
	cat <<EOF
RULE pid $pid
SLOT 3

RULE cmd curl
RULE comm curl
OR
SLOT 3

RULE cmd wget
RULE comm wget
OR
SLOT 5

RULE exec e2e-split-exec-client
SLOT 5

RULE user almrouteuser
SLOT 3

RULE uid 24011
SLOT 5

RULE group almroutegroup
SLOT 3

RULE gid 24013
SLOT 5

TRUE
SLOT 6
EOF
}

assert_body() {
	local file="$1"
	local want="$2"
	local got
	got="$(cat "$file")"
	[[ "$got" == "$want" ]] || fail "unexpected response body: $got"
}

effective_default_tun_field() {
	local field="$1"
	cli graph menu --node "$default_tun" | jq -er --arg field "$field" '
		.blocks[] | select(.id == "effective")
		| .items[] | select(.id == $field)
		| (.value // .text // "")
	'
}

default_tun_field_equals() {
	local field="$1"
	local want="$2"
	[[ "$(effective_default_tun_field "$field")" == "$want" ]]
}

assert_default_tun_feature_surface() {
	local policy="$1"
	local menu="$E2E_RUNTIME/default-tun-menu-$policy.json"
	cli graph menu --node "$default_tun" >"$menu"
	jq -e '
		["tunAddrs", "tunRoutes", "mtu", "dnsIP", "strict", "exclude", "include"] as $required
		| [.blocks[] | select(.id == "config") | .items[] | .id] as $actual
		| all($required[]; . as $id | $actual | index($id) != null) and
		  all(.blocks[] | select(.id == "config") | .items[]
		      | select(.id == "strict" or .id == "exclude" or .id == "include");
		      (.readonly // false) == false)
	' "$menu" >/dev/null \
		|| fail "DefaultTun Linux feature surface is unavailable in $policy isolation"
	[[ -z "$(effective_default_tun_field error)" ]] \
		|| fail "DefaultTun reported a runtime error in $policy isolation"

	cli graph apply-menu --node "$default_tun" --json '{"config":{"mtu":1400}}' \
		>"$E2E_RUNTIME/default-tun-mtu-$policy.json"
	retry "$ALMAGEST_E2E_TIMEOUT" "DefaultTun MTU update in $policy isolation" \
		default_tun_field_equals mtu 1400 \
		|| fail "DefaultTun dynamic MTU is unavailable in $policy isolation"
	cli graph apply-menu --node "$default_tun" --json '{"config":{"mtu":0}}' >/dev/null
}

assert_default_tun_rule_matrix() {
	local policy="$1"
	local context other strict rules payload expected
	rules="$(jq -nc --arg pid "$$" '
		[
			["comm", "^bash$"],
			["exec", "bash"],
			["cmd", "tun_splitter_process_routing"],
			["pid", $pid],
			["user", "root"],
			["uid", "0"],
			["group", "root"],
			["gid", "0"]
		]
		| to_entries
		| map({
			id: ("rule-" + (.key | tostring)),
			template: "rule",
			values: {type: .value[0], rule: .value[1]}
		})
	')"
	expected=$'comm ^bash$\nexec bash\ncmd tun_splitter_process_routing\npid '"$$"$'\nuser root\nuid 0\ngroup root\ngid 0'

	for context in exclude include; do
		if [[ "$context" == "exclude" ]]; then
			other=include
			strict=false
		else
			other=exclude
			strict=true
		fi
		payload="$(jq -nc \
			--arg context "$context" --arg other "$other" \
			--argjson strict "$strict" --argjson rules "$rules" \
			'{config:{strict:$strict, ($context):$rules, ($other):[]}}')"
		cli graph apply-menu --node "$default_tun" --json "$payload" \
			>"$E2E_RUNTIME/default-tun-$policy-$context.json"
		retry "$ALMAGEST_E2E_TIMEOUT" \
			"DefaultTun $context rule matrix in $policy isolation" \
			default_tun_field_equals "$context" "$expected" \
			|| fail "DefaultTun $context rules lost a sysnet-linux rule type in $policy isolation"
		[[ -z "$(effective_default_tun_field error)" ]] \
			|| fail "DefaultTun rejected $context rules in $policy isolation"
	done
	cli graph apply-menu --node "$default_tun" --json \
		'{"config":{"strict":false,"exclude":[],"include":[]}}' >/dev/null
}

exercise_splitter_rules() {
	local policy="$1"
	local pid_rule rules

	retry "$ALMAGEST_E2E_TIMEOUT" "curl routed to Server 1 slot in $policy isolation" \
		curl_split_target "$E2E_RUNTIME/$policy-curl.txt" \
		|| fail "curl request to split target failed in $policy isolation"
	assert_body "$E2E_RUNTIME/$policy-curl.txt" "Server 1"

	retry "$ALMAGEST_E2E_TIMEOUT" "wget routed to Server 2 slot in $policy isolation" \
		wget_split_target "$E2E_RUNTIME/$policy-wget.txt" \
		|| fail "wget request to split target failed in $policy isolation"
	assert_body "$E2E_RUNTIME/$policy-wget.txt" "Server 2"

	retry "$ALMAGEST_E2E_TIMEOUT" "exec rule in $policy isolation" \
		exec_split_target "$E2E_RUNTIME/$policy-exec.txt" \
		|| fail "exec-rule request failed in $policy isolation"
	assert_body "$E2E_RUNTIME/$policy-exec.txt" "Server 2"

	retry "$ALMAGEST_E2E_TIMEOUT" "user rule in $policy isolation" \
		python_split_target_as_uid 24010 "/tmp/e2e-$policy-user.txt" \
		|| fail "user-rule request failed in $policy isolation"
	assert_body "/tmp/e2e-$policy-user.txt" "Server 1"

	retry "$ALMAGEST_E2E_TIMEOUT" "uid rule in $policy isolation" \
		python_split_target_as_uid 24011 "/tmp/e2e-$policy-uid.txt" \
		|| fail "uid-rule request failed in $policy isolation"
	assert_body "/tmp/e2e-$policy-uid.txt" "Server 2"

	retry "$ALMAGEST_E2E_TIMEOUT" "group rule in $policy isolation" \
		python_split_target_as_uid 24012 "/tmp/e2e-$policy-group.txt" \
		|| fail "group-rule request failed in $policy isolation"
	assert_body "/tmp/e2e-$policy-group.txt" "Server 1"

	retry "$ALMAGEST_E2E_TIMEOUT" "gid rule in $policy isolation" \
		python_split_target_as_uid 24013 "/tmp/e2e-$policy-gid.txt" \
		|| fail "gid-rule request failed in $policy isolation"
	assert_body "/tmp/e2e-$policy-gid.txt" "Server 2"

	start_pid_split_client \
		"$E2E_RUNTIME/$policy-pid.ready" \
		"$E2E_RUNTIME/$policy-pid.trigger" \
		"$E2E_RUNTIME/$policy-pid.txt" \
		"$E2E_RUNTIME/$policy-pid.err"
	retry "$ALMAGEST_E2E_TIMEOUT" "pid split client ready in $policy isolation" \
		test -s "$E2E_RUNTIME/$policy-pid.ready" \
		|| fail "pid split client did not become ready in $policy isolation"
	pid_rule="$(cat "$E2E_RUNTIME/$policy-pid.ready")"
	rules="$(splitter_rules_with_pid "$pid_rule")"
	jq -n --arg rules "$rules" '{"config":{"rules":$rules}}' \
		| cli graph apply-menu --node "$splitter" >"$E2E_RUNTIME/$policy-pid-rules.json"
	touch "$E2E_RUNTIME/$policy-pid.trigger"
	wait_process "$pid_client_pid" \
		|| fail "pid-rule client did not exit cleanly in $policy isolation"
	pid_client_pid=""
	assert_body "$E2E_RUNTIME/$policy-pid.txt" "Server 1"

	retry "$ALMAGEST_E2E_TIMEOUT" "OutNetwork fallback in $policy isolation" \
		python_http_target "/outnetwork-fallback-$policy" "$E2E_RUNTIME/$policy-outnetwork.txt" \
		|| fail "fallback traffic failed in $policy isolation"
	assert_body "$E2E_RUNTIME/$policy-outnetwork.txt" "$public_body"
}

run_isolation_policy() {
	local policy="$1"
	export ALMAGEST_ISOLATION="$policy"
	start_daemon
	jq -e --arg policy "$policy" '
		.execution_mode == "isolation" and .isolation.requested_policy == $policy
	' /tmp/e2e-hello.json >/dev/null \
		|| fail "daemon did not select $policy isolation"

	for class in DefaultTun TunSplitter TunCopy VTun Spoofer OutNetwork MockNetwork HttpServer; do
		wait_node_class_available "almagest/$class" \
			|| fail "$class node class is unavailable in $policy isolation"
	done
	for node in "DefaultTun 1501" "TunSplitter 1502" "TunCopy 1505" "VTun 1506" \
		"Spoofer 1503" "Spoofer 1508"; do
		wait_graph_node "$node" || fail "$node is missing in $policy isolation"
	done

	default_tun="$(node_id_by_name "DefaultTun 1501")"
	splitter="$(node_id_by_name "TunSplitter 1502")"
	copy="$(node_id_by_name "TunCopy 1505")"
	curl_spoofer="$(node_id_by_name "Spoofer 1503")"
	out_spoofer="$(node_id_by_name "Spoofer 1508")"
	wait_node_label "$default_tun" ready || fail "DefaultTun did not become ready"
	wait_node_label "$splitter" splitting || fail "TunSplitter did not start"
	wait_node_label "$copy" copying || fail "TunCopy did not start"
	wait_node_label "$curl_spoofer" forwarding || fail "curl Spoofer did not start"
	wait_node_label "$out_spoofer" forwarding || fail "fallback Spoofer did not start"
	wait_log_contains "almagest/HttpServer" "listening on 10.210.148.1:8080" \
		|| fail "internal HTTP servers did not start in $policy isolation"

	assert_default_tun_feature_surface "$policy"
	assert_default_tun_rule_matrix "$policy"
	exercise_splitter_rules "$policy"
	stop_daemon
}

public_pid="$(start_static_http_target_in_netns "$public_ns" "$public_host" "$public_port" "$public_body" "$public_log")"
trap cleanup EXIT

retry "$ALMAGEST_E2E_TIMEOUT" "baseline public target before daemon" \
	python_http_target /baseline /tmp/e2e-tun-splitter-public-baseline.txt \
	|| fail "public target was not reachable before daemon startup"
assert_body /tmp/e2e-tun-splitter-public-baseline.txt "$public_body"

run_isolation_policy auto
run_isolation_policy strict

retry "$ALMAGEST_E2E_TIMEOUT" "public target after daemon stop" \
	python_http_target /after-daemon-stop /tmp/e2e-tun-splitter-public-stopped.txt \
	|| fail "public target was not reachable after daemon shutdown"
assert_body /tmp/e2e-tun-splitter-public-stopped.txt "$public_body"

grep -F "GET /baseline" "$public_log" >/dev/null \
	|| fail "public target did not receive baseline request"
for policy in auto strict; do
	grep -F "GET /outnetwork-fallback-$policy" "$public_log" >/dev/null \
		|| fail "public target did not receive $policy isolation fallback request"
done
grep -F "GET /after-daemon-stop" "$public_log" >/dev/null \
	|| fail "public target did not receive post-daemon request"

log "case passed"
