#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

first_port=18101
second_port=18102
first_body="network firewall service one"
second_body="network firewall service two"
first_log="/tmp/e2e-network-firewall-service-one.log"
second_log="/tmp/e2e-network-firewall-service-two.log"
first_pid=""
second_pid=""

cleanup() {
	stop_static_http_target "$first_pid"
	stop_static_http_target "$second_pid"
	stop_daemon
}

curl_direct() {
	local port="$1"
	local path="$2"
	local output="$3"
	curl --fail --silent --show-error --noproxy '*' \
		--connect-timeout 1 --max-time 3 \
		"http://127.0.0.1:${port}${path}" \
		--output "$output"
}

curl_through_firewall() {
	local port="$1"
	local path="$2"
	local output="$3"
	curl --fail --silent --show-error \
		--socks5-hostname 127.0.0.1:1191 \
		--connect-timeout 1 --max-time 3 \
		"http://127.0.0.1:${port}${path}" \
		--output "$output"
}

firewall_request_fails() {
	local port="$1"
	if curl_through_firewall "$port" /unexpected \
		/tmp/e2e-network-firewall-unexpected-body.txt \
		>/tmp/e2e-network-firewall-unexpected-stdout.txt \
		2>/tmp/e2e-network-firewall-expected-failure.txt; then
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
		>/tmp/e2e-network-firewall-apply-rules.json
}

apply_object_policy() {
	local node="$1"
	local value="$2"
	jq -n --arg value "$value" '{"state":{"value":$value}}' \
		| cli graph apply-menu --node "$node" \
		>/tmp/e2e-network-firewall-apply-object.json
}

first_pid="$(start_static_http_target 127.0.0.1 "$first_port" "$first_body" "$first_log")"
second_pid="$(start_static_http_target 127.0.0.1 "$second_port" "$second_body" "$second_log")"
trap cleanup EXIT

# Check the real services without Almagest before testing the middleware path.
retry "$ALMAGEST_E2E_TIMEOUT" "first OS HTTP service" \
	curl_direct "$first_port" /direct /tmp/e2e-network-firewall-first-direct.txt \
	|| fail "first OS HTTP service did not start"
retry "$ALMAGEST_E2E_TIMEOUT" "second OS HTTP service" \
	curl_direct "$second_port" /direct /tmp/e2e-network-firewall-second-direct.txt \
	|| fail "second OS HTTP service did not start"
assert_body /tmp/e2e-network-firewall-first-direct.txt "$first_body"
assert_body /tmp/e2e-network-firewall-second-direct.txt "$second_body"

start_daemon

wait_node_class_available "almagest/NetworkFirewall" \
	|| fail "NetworkFirewall node class is unavailable"
wait_graph_node "Firewall curl proxy" || fail "LocalProxy node missing"
wait_graph_node "Network Firewall curl" || fail "NetworkFirewall node missing"
wait_graph_node "Firewall object policy" || fail "ObjectConstant node missing"
wait_graph_node "Firewall OutNetwork" || fail "OutNetwork node missing"

firewall="$(node_id_by_name "Network Firewall curl")"
object_policy="$(node_id_by_name "Firewall object policy")"
wait_node_label "$firewall" "filtering 0 rules" \
	|| fail "NetworkFirewall did not become ready"
wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:1191" \
	|| fail "firewall SOCKS proxy did not start"

# An empty menu and empty object policy allow both new outgoing TCP flows.
retry "$ALMAGEST_E2E_TIMEOUT" "first service through empty firewall" \
	curl_through_firewall "$first_port" /initial \
	/tmp/e2e-network-firewall-first-initial.txt \
	|| fail "empty NetworkFirewall blocked the first service"
retry "$ALMAGEST_E2E_TIMEOUT" "second service through empty firewall" \
	curl_through_firewall "$second_port" /initial \
	/tmp/e2e-network-firewall-second-initial.txt \
	|| fail "empty NetworkFirewall blocked the second service"
assert_body /tmp/e2e-network-firewall-first-initial.txt "$first_body"
assert_body /tmp/e2e-network-firewall-second-initial.txt "$second_body"

# The object input blocks only the first destination port.
apply_object_policy "$object_policy" \
	"{\"exclude\":[{\"network\":\"tcp\",\"ports\":[${first_port}]}]}"
wait_node_label "$firewall" "filtering 1 rules" \
	|| fail "NetworkFirewall did not install the object rule"
retry "$ALMAGEST_E2E_TIMEOUT" "object policy blocks first service" \
	firewall_request_fails "$first_port" \
	|| fail "object policy did not block the first service"
retry "$ALMAGEST_E2E_TIMEOUT" "object policy allows second service" \
	curl_through_firewall "$second_port" /object-allows \
	/tmp/e2e-network-firewall-second-object.txt \
	|| fail "object policy blocked the unrelated second service"
assert_body /tmp/e2e-network-firewall-second-object.txt "$second_body"

# An invalid object replaces only that link's previous contribution. It must
# report the invalid source without leaving the old block installed. A later
# valid value on the same link must recover the policy.
apply_object_policy "$object_policy" '{"exclude":"invalid"}'
wait_node_label "$firewall" "invalid object config" \
	|| fail "NetworkFirewall did not report the invalid object policy"
retry "$ALMAGEST_E2E_TIMEOUT" "first service after invalid object policy" \
	curl_through_firewall "$first_port" /invalid-object \
	/tmp/e2e-network-firewall-first-invalid-object.txt \
	|| fail "invalid object policy left its previous rule installed"
assert_body /tmp/e2e-network-firewall-first-invalid-object.txt "$first_body"
apply_object_policy "$object_policy" \
	"{\"exclude\":[{\"network\":\"tcp\",\"ports\":[${first_port}]}]}"
wait_node_label "$firewall" "filtering 1 rules" \
	|| fail "NetworkFirewall did not recover from the invalid object policy"
retry "$ALMAGEST_E2E_TIMEOUT" "restored object policy blocks first service" \
	firewall_request_fails "$first_port" \
	|| fail "restored object policy did not block the first service"

# A menu rule adds a second block. The effective policy is the union of both
# sources, so neither service is reachable through the firewall.
apply_firewall_rules "$firewall" \
	"{\"exclude\":[{\"network\":\"tcp\",\"ports\":[${second_port}]}]}"
retry "$ALMAGEST_E2E_TIMEOUT" "merged policy still blocks first service" \
	firewall_request_fails "$first_port" \
	|| fail "menu update removed the object policy"
retry "$ALMAGEST_E2E_TIMEOUT" "menu policy blocks second service" \
	firewall_request_fails "$second_port" \
	|| fail "menu policy did not block the second service"

# A rejected menu edit must keep both last valid policies active. Restoring a
# valid menu value clears the form error without replacing the object policy.
apply_firewall_rules "$firewall" '{not valid firewall JSON'
wait_node_label "$firewall" "invalid config" \
	|| fail "NetworkFirewall did not report the invalid menu policy"
retry "$ALMAGEST_E2E_TIMEOUT" "object rule survives invalid menu policy" \
	firewall_request_fails "$first_port" \
	|| fail "invalid menu policy removed the object rule"
retry "$ALMAGEST_E2E_TIMEOUT" "menu rule survives invalid menu policy" \
	firewall_request_fails "$second_port" \
	|| fail "invalid menu policy replaced the last valid menu rule"
apply_firewall_rules "$firewall" \
	"{\"exclude\":[{\"network\":\"tcp\",\"ports\":[${second_port}]}]}"
wait_node_label "$firewall" "filtering 1 rules" \
	|| fail "NetworkFirewall did not recover from the invalid menu policy"

# Remove only the object contribution. The first port is allowed again, while
# the independent menu contribution continues to block the second port.
apply_object_policy "$object_policy" "{}"
wait_node_label "$firewall" "filtering 1 rules" \
	|| fail "NetworkFirewall did not remove the object rule"
retry "$ALMAGEST_E2E_TIMEOUT" "first service allowed after object clear" \
	curl_through_firewall "$first_port" /object-cleared \
	/tmp/e2e-network-firewall-first-restored.txt \
	|| fail "first service did not recover after object policy clear"
assert_body /tmp/e2e-network-firewall-first-restored.txt "$first_body"
retry "$ALMAGEST_E2E_TIMEOUT" "menu still blocks second service" \
	firewall_request_fails "$second_port" \
	|| fail "clearing the object policy also removed the menu policy"

# Remove the last rule and prove that both real services are allowed again.
apply_firewall_rules "$firewall" "{}"
wait_node_label "$firewall" "filtering 0 rules" \
	|| fail "NetworkFirewall did not clear its menu rule"
retry "$ALMAGEST_E2E_TIMEOUT" "first service after all rules clear" \
	curl_through_firewall "$first_port" /restored \
	/tmp/e2e-network-firewall-first-final.txt \
	|| fail "first service was not reachable after policy clear"
retry "$ALMAGEST_E2E_TIMEOUT" "second service after all rules clear" \
	curl_through_firewall "$second_port" /restored \
	/tmp/e2e-network-firewall-second-final.txt \
	|| fail "second service was not reachable after policy clear"
assert_body /tmp/e2e-network-firewall-first-final.txt "$first_body"
assert_body /tmp/e2e-network-firewall-second-final.txt "$second_body"

# Link removal must withdraw a current object contribution. Reattaching the
# same constant must request its latest value and reinstall that contribution.
apply_object_policy "$object_policy" \
	"{\"exclude\":[{\"network\":\"tcp\",\"ports\":[${first_port}]}]}"
wait_node_label "$firewall" "filtering 1 rules" \
	|| fail "NetworkFirewall did not reinstall the object rule"
object_link="$(link_id_by_port_names \
	"Firewall object policy" output "Network Firewall curl" Config)"
cli graph delete-link --id "$object_link" >/dev/null
wait_node_label "$firewall" "filtering 0 rules" \
	|| fail "NetworkFirewall kept an object rule after its link was removed"
retry "$ALMAGEST_E2E_TIMEOUT" "first service after object link removal" \
	curl_through_firewall "$first_port" /object-detached \
	/tmp/e2e-network-firewall-first-object-detached.txt \
	|| fail "first service did not recover after object link removal"
add_link_by_port_name "$firewall" left Config "$object_policy" right output >/dev/null
wait_node_label "$firewall" "filtering 1 rules" \
	|| fail "NetworkFirewall did not request the reattached object value"
retry "$ALMAGEST_E2E_TIMEOUT" "reattached object policy blocks first service" \
	firewall_request_fails "$first_port" \
	|| fail "reattached object policy did not block the first service"
apply_object_policy "$object_policy" "{}"
wait_node_label "$firewall" "filtering 0 rules" \
	|| fail "NetworkFirewall did not clear the reattached object rule"

# Replacing the upstream network must close the old firewall wrapper. The
# restored edge must provide a new wrapper that works on its first requests.
out_network="$(node_id_by_name "Firewall OutNetwork")"
upstream_link="$(link_id_by_port_names \
	"Network Firewall curl" Network "Firewall OutNetwork" Network)"
cli graph delete-link --id "$upstream_link" >/dev/null
wait_node_label "$firewall" "waiting for network" \
	|| fail "NetworkFirewall did not wait after upstream removal"
retry "$ALMAGEST_E2E_TIMEOUT" "first service fails without firewall upstream" \
	firewall_request_fails "$first_port" \
	|| fail "old firewall wrapper stayed usable after upstream removal"
add_link_by_port_name "$firewall" right Network "$out_network" left Network >/dev/null
wait_node_label "$firewall" "filtering 0 rules" \
	|| fail "NetworkFirewall did not attach its replacement upstream"
retry "$ALMAGEST_E2E_TIMEOUT" "first service after upstream replacement" \
	curl_through_firewall "$first_port" /upstream-restored \
	/tmp/e2e-network-firewall-first-upstream-restored.txt \
	|| fail "first request failed after firewall upstream replacement"
retry "$ALMAGEST_E2E_TIMEOUT" "second service after upstream replacement" \
	curl_through_firewall "$second_port" /upstream-restored \
	/tmp/e2e-network-firewall-second-upstream-restored.txt \
	|| fail "second request failed after firewall upstream replacement"

# A final menu policy and all restored links must survive daemon restart.
apply_firewall_rules "$firewall" \
	"{\"exclude\":[{\"network\":\"tcp\",\"ports\":[${second_port}]}]}"
wait_node_label "$firewall" "filtering 1 rules" \
	|| fail "NetworkFirewall did not install the pre-restart policy"
stop_daemon
start_daemon
firewall="$(node_id_by_name "Network Firewall curl")"
wait_node_label "$firewall" "filtering 1 rules" \
	|| fail "NetworkFirewall did not restore its saved policy"
retry "$ALMAGEST_E2E_TIMEOUT" "first service after firewall restart" \
	curl_through_firewall "$first_port" /restart-allowed \
	/tmp/e2e-network-firewall-first-restart.txt \
	|| fail "allowed service did not recover after daemon restart"
retry "$ALMAGEST_E2E_TIMEOUT" "saved menu policy after firewall restart" \
	firewall_request_fails "$second_port" \
	|| fail "saved firewall policy did not survive daemon restart"

grep -F "GET /restored" "$first_log" >/dev/null \
	|| fail "first service did not record the restored request"
grep -F "GET /restored" "$second_log" >/dev/null \
	|| fail "second service did not record the restored request"

log "case passed"
