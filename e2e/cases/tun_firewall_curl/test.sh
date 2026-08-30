#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

target_host="10.213.17.2"
first_port=18111
second_port=18112
first_body="tun firewall service one"
second_body="tun firewall service two"

cleanup() {
	stop_daemon
	if [[ -x "$E2E_RUNTIME/cleanup-tun-firewall.sh" ]]; then
		"$E2E_RUNTIME/cleanup-tun-firewall.sh"
	fi
}

curl_through_tun() {
	local port="$1"
	local path="$2"
	local output="$3"
	curl --fail --silent --show-error --noproxy '*' \
		--connect-timeout 1 --max-time 3 \
		"http://${target_host}:${port}${path}" \
		--output "$output"
}

tun_request_fails() {
	local port="$1"
	if curl_through_tun "$port" /unexpected \
		/tmp/e2e-tun-firewall-unexpected-body.txt \
		>/tmp/e2e-tun-firewall-unexpected-stdout.txt \
		2>/tmp/e2e-tun-firewall-expected-failure.txt; then
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

apply_firewall_rules() {
	local node="$1"
	local rules="$2"
	jq -n --arg rules "$rules" '{"config":{"rules":$rules}}' \
		| cli graph apply-menu --node "$node" \
		>/tmp/e2e-tun-firewall-apply-rules.json
}

apply_object_policy() {
	local node="$1"
	local value="$2"
	jq -n --arg value "$value" '{"state":{"value":$value}}' \
		| cli graph apply-menu --node "$node" \
		>/tmp/e2e-tun-firewall-apply-object.json
}

trap cleanup EXIT
start_daemon

wait_node_class_available "almagest/TunFirewall" \
	|| fail "TunFirewall node class is unavailable"
wait_graph_node "Firewall Tun" || fail "Tun node missing"
wait_graph_node "TUN Firewall curl" || fail "TunFirewall node missing"
wait_graph_node "TUN firewall object policy" || fail "ObjectConstant node missing"
wait_graph_node "Firewall Spoofer" || fail "Spoofer node missing"
wait_graph_node "Firewall MockNetwork" || fail "MockNetwork node missing"
wait_graph_node "Firewall HTTP service one" || fail "first HttpServer node missing"
wait_graph_node "Firewall HTTP service two" || fail "second HttpServer node missing"

tun="$(node_id_by_name "Firewall Tun")"
firewall="$(node_id_by_name "TUN Firewall curl")"
object_policy="$(node_id_by_name "TUN firewall object policy")"
spoofer="$(node_id_by_name "Firewall Spoofer")"

wait_node_label "$tun" almfw0 || fail "Tun did not create almfw0"
wait_node_label "$firewall" "filtering 0 rules" \
	|| fail "TunFirewall did not become ready"
wait_node_label "$spoofer" forwarding || fail "Spoofer did not start forwarding"
wait_log_contains "almagest/HttpServer" "listening on ${target_host}:${first_port}" \
	|| fail "first TUN-backed HTTP service did not start"
wait_log_contains "almagest/HttpServer" "listening on ${target_host}:${second_port}" \
	|| fail "second TUN-backed HTTP service did not start"

# Real curl packets leave the container IP stack through almfw0 and pass the
# empty Tun Firewall to the two userspace HTTP services.
retry "$ALMAGEST_E2E_TIMEOUT" "first service through empty TUN firewall" \
	curl_through_tun "$first_port" /initial /tmp/e2e-tun-firewall-first-initial.txt \
	|| fail "empty TunFirewall blocked the first service"
retry "$ALMAGEST_E2E_TIMEOUT" "second service through empty TUN firewall" \
	curl_through_tun "$second_port" /initial /tmp/e2e-tun-firewall-second-initial.txt \
	|| fail "empty TunFirewall blocked the second service"
assert_body /tmp/e2e-tun-firewall-first-initial.txt "$first_body"
assert_body /tmp/e2e-tun-firewall-second-initial.txt "$second_body"

# Add one policy through the object link. New TCP packets to the first service
# are dropped, but the second service stays reachable through the same TUN.
apply_object_policy "$object_policy" \
	"{\"exclude\":[{\"network\":\"tcp\",\"ports\":[${first_port}]}]}"
wait_node_label "$firewall" "filtering 1 rules" \
	|| fail "TunFirewall did not install the object rule"
retry "$ALMAGEST_E2E_TIMEOUT" "object policy blocks first TUN service" \
	tun_request_fails "$first_port" \
	|| fail "object policy did not block the first TUN service"
retry "$ALMAGEST_E2E_TIMEOUT" "object policy allows second TUN service" \
	curl_through_tun "$second_port" /object-allows \
	/tmp/e2e-tun-firewall-second-object.txt \
	|| fail "object policy blocked the unrelated second TUN service"
assert_body /tmp/e2e-tun-firewall-second-object.txt "$second_body"

# Add an independent menu rule. Both rules form the effective policy, so both
# service ports are now blocked.
apply_firewall_rules "$firewall" \
	"{\"exclude\":[{\"network\":\"tcp\",\"ports\":[${second_port}]}]}"
retry "$ALMAGEST_E2E_TIMEOUT" "merged policy still blocks first TUN service" \
	tun_request_fails "$first_port" \
	|| fail "menu update removed the TUN object policy"
retry "$ALMAGEST_E2E_TIMEOUT" "menu policy blocks second TUN service" \
	tun_request_fails "$second_port" \
	|| fail "menu policy did not block the second TUN service"

# Clear only the object rule. Port 18111 recovers, while the menu rule for
# port 18112 remains effective.
apply_object_policy "$object_policy" "{}"
wait_node_label "$firewall" "filtering 1 rules" \
	|| fail "TunFirewall did not remove the object rule"
retry "$ALMAGEST_E2E_TIMEOUT" "first TUN service after object clear" \
	curl_through_tun "$first_port" /object-cleared \
	/tmp/e2e-tun-firewall-first-restored.txt \
	|| fail "first TUN service did not recover after object policy clear"
assert_body /tmp/e2e-tun-firewall-first-restored.txt "$first_body"
retry "$ALMAGEST_E2E_TIMEOUT" "menu still blocks second TUN service" \
	tun_request_fails "$second_port" \
	|| fail "clearing the TUN object policy also removed the menu policy"

# Clear the final menu rule and verify both services through the same live TUN
# path. This is the final allow state.
apply_firewall_rules "$firewall" "{}"
wait_node_label "$firewall" "filtering 0 rules" \
	|| fail "TunFirewall did not clear its menu rule"
retry "$ALMAGEST_E2E_TIMEOUT" "first TUN service after all rules clear" \
	curl_through_tun "$first_port" /restored /tmp/e2e-tun-firewall-first-final.txt \
	|| fail "first TUN service was not reachable after policy clear"
retry "$ALMAGEST_E2E_TIMEOUT" "second TUN service after all rules clear" \
	curl_through_tun "$second_port" /restored /tmp/e2e-tun-firewall-second-final.txt \
	|| fail "second TUN service was not reachable after policy clear"
assert_body /tmp/e2e-tun-firewall-first-final.txt "$first_body"
assert_body /tmp/e2e-tun-firewall-second-final.txt "$second_body"

wait_log_contains "almagest/HttpServer" "GET /restored" \
	|| fail "TUN-backed services did not log the restored requests"

log "case passed"

