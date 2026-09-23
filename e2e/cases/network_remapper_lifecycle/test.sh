#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

rules_for_port() {
	local port="$1"
	printf 'DIAL\nADDR_S service.test\nAND\nPORT 80\nAND\nREMAP DST ADDR_PORT 127.0.0.1:%s' "$port"
}

apply_rules() {
	local rules="$1"
	jq -n --arg rules "$rules" '{"config":{"rules":$rules}}' \
		| cli graph apply-menu --name "Remapper" >/dev/null
}

socks_body_is() {
	local socks_port="$1"
	local expected="$2"
	local output="/tmp/remapper-${socks_port}.body"

	curl --fail --silent --show-error --max-time 2 \
		--socks5-hostname "127.0.0.1:$socks_port" \
		http://service.test:80/ \
		--output "$output" \
		&& [[ "$(<"$output")" == "$expected" ]]
}

assert_socks_body() {
	local socks_port="$1"
	local expected="$2"

	retry "$ALMAGEST_E2E_TIMEOUT" "SOCKS $socks_port returns $expected" \
		socks_body_is "$socks_port" "$expected" \
		|| fail "SOCKS $socks_port did not return $expected"
}

socks_request_fails() {
	local socks_port="$1"
	! curl --fail --silent --show-error --max-time 1 \
		--socks5-hostname "127.0.0.1:$socks_port" \
		http://service.test:80/ \
		--output /dev/null \
		2>/dev/null
}

assert_socks_failure() {
	local socks_port="$1"
	retry "$ALMAGEST_E2E_TIMEOUT" "SOCKS $socks_port becomes unavailable" \
		socks_request_fails "$socks_port" \
		|| fail "SOCKS $socks_port stayed usable without a network payload"
}

start_daemon

for node in \
	"Proxy One" \
	"Proxy Two" \
	"Remapper" \
	"Mock A" \
	"Mock B" \
	"Server A" \
	"Server A Alternate" \
	"Server B"; do
	wait_graph_node "$node" || fail "$node node missing"
done

remapper="$(node_id_by_name "Remapper")"
mock_a="$(node_id_by_name "Mock A")"
mock_b="$(node_id_by_name "Mock B")"
proxy_one="$(node_id_by_name "Proxy One")"

wait_node_label "$remapper" "remapping 1 rule" \
	|| fail "Remapper did not accept its initial rule"
assert_socks_body 1181 "provider-a-port-8080"
assert_socks_body 1182 "provider-a-port-8080"

# One live edit must replace every per-link wrapper.
apply_rules "$(rules_for_port 8081)"
wait_node_label "$remapper" "remapping 1 rule" \
	|| fail "Remapper did not accept the alternate-port rule"
assert_socks_body 1181 "provider-a-port-8081"
assert_socks_body 1182 "provider-a-port-8081"

# Invalid text must not destroy the last usable runtime wrappers.
apply_rules $'TRUE\nREMAP DST PORT'
wait_node_label "$remapper" "invalid config" \
	|| fail "Remapper did not report invalid rules"
assert_socks_body 1181 "provider-a-port-8081"
assert_socks_body 1182 "provider-a-port-8081"

# Repeated replacement exercises closure and refresh ordering for both output
# links. Each edit must converge on the newest wrapper before the next edit.
for port in 8080 8081 8080 8081 8080 8081; do
	apply_rules "$(rules_for_port "$port")"
	assert_socks_body 1181 "provider-a-port-$port"
	assert_socks_body 1182 "provider-a-port-$port"
done

apply_rules "$(rules_for_port 8080)"
assert_socks_body 1181 "provider-a-port-8080"
assert_socks_body 1182 "provider-a-port-8080"

# Removing one consumer must close only its wrapper.
proxy_one_link="$(link_id_by_port_names "Proxy One" proxy "Remapper" Network)"
cli graph delete-link --id "$proxy_one_link" >/dev/null
assert_socks_failure 1181
assert_socks_body 1182 "provider-a-port-8080"
add_link_by_port_name "$proxy_one" right proxy "$remapper" left Network >/dev/null
assert_socks_body 1181 "provider-a-port-8080"
assert_socks_body 1182 "provider-a-port-8080"

# Removing the provider invalidates every emitted wrapper. A different provider
# on the same input port must then rebuild both consumers.
input_link="$(link_id_by_port_names "Remapper" Network "Mock A" Network)"
cli graph delete-link --id "$input_link" >/dev/null
wait_node_label "$remapper" "waiting for network" \
	|| fail "Remapper did not notice its removed input"
assert_socks_failure 1181
assert_socks_failure 1182

add_link_by_port_name "$remapper" right Network "$mock_b" left Network >/dev/null
wait_node_label "$remapper" "remapping 1 rule" \
	|| fail "Remapper did not accept its replacement input"
assert_socks_body 1181 "provider-b-port-8080"
assert_socks_body 1182 "provider-b-port-8080"

# Reattach the original source once more to ensure source identity can cycle.
input_link="$(link_id_by_port_names "Remapper" Network "Mock B" Network)"
cli graph delete-link --id "$input_link" >/dev/null
add_link_by_port_name "$remapper" right Network "$mock_a" left Network >/dev/null
assert_socks_body 1181 "provider-a-port-8080"
assert_socks_body 1182 "provider-a-port-8080"

log "case passed"
