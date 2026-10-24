#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

target_body="LocalProxy client filtering target"
target_log="/tmp/e2e-local-proxy-filter-target.log"
target_pid=""
proxy_port=1189
target_port=18189

cleanup() {
	stop_static_http_target "$target_pid"
	stop_daemon
}

proxy_get() {
	local client="$1"
	local path="$2"
	local output="$3"
	"$client" --fail --silent --show-error --noproxy '' \
		--connect-timeout 1 --max-time 3 \
		--socks5-hostname "127.0.0.1:$proxy_port" \
		"http://127.0.0.1:$target_port$path" --output "$output"
}

proxy_get_as_test_user() {
	local path="$1"
	local output="$2"
	rm -f "$output"
	run_as_uid 24021 24021 curl --fail --silent --show-error --noproxy '' \
		--connect-timeout 1 --max-time 3 \
		--socks5-hostname "127.0.0.1:$proxy_port" \
		"http://127.0.0.1:$target_port$path" --output "$output"
}

proxy_get_fails() {
	! proxy_get "$@" >/dev/null 2>&1
}

apply_client_rule() {
	local mode="$1"
	local type="$2"
	local value="$3"
	jq -n --arg mode "$mode" --arg type "$type" --arg value "$value" '
		{config:{exclude:[],include:[]}}
		| .config[$mode] = [{id:"rule-0",template:"rule",values:{type:$type,rule:$value}}]
	' | cli graph apply-menu --node "$proxy" >"$E2E_RUNTIME/apply-$mode-$type.json"
}

clear_client_rules() {
	cli graph apply-menu --node "$proxy" --json '{"config":{"exclude":[],"include":[]}}' \
		>"$E2E_RUNTIME/clear-rules.json"
}

local_proxy_listen_count() {
	cli log subscribe | jq '[.entries[] | select(
		.node_class == "almagest/LocalProxy" and
		(.message | contains("socks listening on 127.0.0.1:1189"))
	)] | length'
}

wait_for_proxy_restart() {
	local previous="$1"
	retry "$ALMAGEST_E2E_TIMEOUT" "LocalProxy listener restart" \
		local_proxy_restarted "$previous"
}

local_proxy_restarted() {
	local previous="$1"
	[[ "$(local_proxy_listen_count)" -gt "$previous" ]]
}

start_daemon
trap cleanup EXIT
target_pid="$(start_static_http_target 127.0.0.1 "$target_port" "$target_body" "$target_log")"

wait_graph_node "Filtered local proxy" || fail "LocalProxy node is missing"
proxy="$(node_id_by_name "Filtered local proxy")"
wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:$proxy_port" \
	|| fail "LocalProxy did not start"

# An omitted or empty policy preserves the old allow-all behavior.
retry "$ALMAGEST_E2E_TIMEOUT" "unfiltered LocalProxy request" \
	proxy_get curl /unfiltered "/tmp/e2e-local-proxy-unfiltered.txt" \
	|| fail "LocalProxy without rules rejected curl"

# The comm rule is the sysnet-linux per-application matcher. The renamed curl
# binary has a different comm value and must not enter the SOCKS handshake.
listen_count="$(local_proxy_listen_count)"
apply_client_rule include comm '^curl$'
wait_for_proxy_restart "$listen_count" || fail "application policy did not start"
retry "$ALMAGEST_E2E_TIMEOUT" "non-included application rejection" \
	proxy_get_fails /tmp/almproxy-denied /app-denied "/tmp/e2e-local-proxy-app-denied.txt" \
	|| fail "LocalProxy accepted an application outside the include rules"
retry "$ALMAGEST_E2E_TIMEOUT" "included application request" \
	proxy_get curl /app-allowed "/tmp/e2e-local-proxy-app-allowed.txt" \
	|| fail "LocalProxy rejected the included curl application"

# The named user rule resolves to UID 24021. Root is rejected while the same
# curl client running as the selected user is accepted.
listen_count="$(local_proxy_listen_count)"
apply_client_rule include user almproxyuser
wait_for_proxy_restart "$listen_count" || fail "user policy did not start"
retry "$ALMAGEST_E2E_TIMEOUT" "non-included user rejection" \
	proxy_get_fails curl /user-denied "/tmp/e2e-local-proxy-user-denied.txt" \
	|| fail "LocalProxy accepted a client outside the user include rule"
retry "$ALMAGEST_E2E_TIMEOUT" "included user request" \
	proxy_get_as_test_user /user-allowed "/tmp/e2e-local-proxy-user-allowed.txt" \
	|| fail "LocalProxy rejected the included user"

listen_count="$(local_proxy_listen_count)"
clear_client_rules
wait_for_proxy_restart "$listen_count" || fail "cleared policy did not restart"
retry "$ALMAGEST_E2E_TIMEOUT" "request after clearing client rules" \
	proxy_get /tmp/almproxy-denied /cleared "/tmp/e2e-local-proxy-cleared.txt" \
	|| fail "LocalProxy did not restore allow-all behavior"

for output in \
	/tmp/e2e-local-proxy-unfiltered.txt \
	/tmp/e2e-local-proxy-app-allowed.txt \
	/tmp/e2e-local-proxy-user-allowed.txt \
	/tmp/e2e-local-proxy-cleared.txt; do
	[[ "$(cat "$output")" == "$target_body" ]] \
		|| fail "unexpected response in $output"
done

grep -F "GET /unfiltered" "$target_log" >/dev/null || fail "target missed unfiltered request"
grep -F "GET /app-allowed" "$target_log" >/dev/null || fail "target missed application request"
grep -F "GET /user-allowed" "$target_log" >/dev/null || fail "target missed user request"
grep -F "GET /cleared" "$target_log" >/dev/null || fail "target missed cleared-policy request"

log "case passed in isolation=$ALMAGEST_E2E_ISOLATION_MODE"
