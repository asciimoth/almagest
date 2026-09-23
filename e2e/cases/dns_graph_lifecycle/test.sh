#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

menu_text() {
	local node="$1"
	local block="$2"
	local field="$3"
	cli graph menu --name "$node" | jq -er \
		--arg block "$block" \
		--arg field "$field" \
		'
		.blocks[]
		| select(.id == $block)
		| .items[]
		| select(.id == $field)
		| (.value // .text // "")
		'
}

dns_query_has_answer() {
	local node="$1"
	local answer="$2"
	[[ "$(menu_text "$node" result status)" == "Status: NOERROR" \
		&& -z "$(menu_text "$node" result error)" \
		&& "$(menu_text "$node" result response)" == *"$answer"* ]]
}

dns_query_has_error() {
	local node="$1"
	local error_text="$2"
	[[ "$(menu_text "$node" result status)" == "Status: fail" \
		&& "$(menu_text "$node" result error)" == *"$error_text"* ]]
}

send_query_expect_answer() {
	local node="$1"
	local answer="$2"
	cli graph click-menu-button --name "$node" --block request --button send >/dev/null
	retry "$ALMAGEST_E2E_TIMEOUT" "$node answer $answer" \
		dns_query_has_answer "$node" "$answer" \
		|| fail "$node did not return $answer"
}

send_query_expect_error() {
	local node="$1"
	local error_text="$2"
	cli graph click-menu-button --name "$node" --block request --button send >/dev/null
	retry "$ALMAGEST_E2E_TIMEOUT" "$node error containing $error_text" \
		dns_query_has_error "$node" "$error_text" \
		|| fail "$node did not fail with an error containing: $error_text"
}

apply_router_rules() {
	local rules="$1"
	jq -n --arg rules "$rules" '{"config":{"rules":$rules}}' \
		| cli graph apply-menu --name "DNS Router" >/dev/null
}

port_exists() {
	local node="$1"
	local direction="$2"
	local name="$3"
	local id
	id="$(node_id_by_name "$node")" || return 1
	node_port_id "$id" "$direction" "$name" >/dev/null
}

port_missing() {
	! port_exists "$1" "$2" "$3"
}

start_daemon

for node in \
	"Empty DNS" \
	"Primary DNS" \
	"Secondary DNS" \
	"Fallback DNS" \
	"DNS Cache" \
	"DNS Router" \
	"Fallback Query" \
	"Cached Query" \
	"Uncached Query" \
	"Router One Query" \
	"Router Two Query" \
	"Invalid Type Query"; do
	wait_graph_node "$node" || fail "$node node missing"
done

fallback="$(node_id_by_name "Fallback DNS")"
cache="$(node_id_by_name "DNS Cache")"
router="$(node_id_by_name "DNS Router")"
primary="$(node_id_by_name "Primary DNS")"
secondary="$(node_id_by_name "Secondary DNS")"
router_one_query="$(node_id_by_name "Router One Query")"

wait_node_label "$fallback" "fallback across 3 DNS backends" \
	|| fail "DnsFallback did not attach all initial backends"
wait_node_label "$cache" "caching DNS" \
	|| fail "DnsCache did not attach its upstream"
wait_node_label "$router" "routing 2 DNS backends" \
	|| fail "DnsRouter did not attach both initial backends"

send_query_expect_answer "Fallback Query" "10.20.0.1"
send_query_expect_answer "Cached Query" "10.20.0.11"
send_query_expect_answer "Router One Query" "10.20.0.21"
send_query_expect_answer "Router Two Query" "10.20.0.32"
send_query_expect_error "Invalid Type Query" "unsupported DNS type"

# The cache keeps a known answer while detached, but it cannot resolve a new
# name until the upstream link returns.
cache_input="$(link_id_by_port_names "DNS Cache" DNS "Fallback DNS" DNS)"
cli graph delete-link --id "$cache_input" >/dev/null
wait_node_label "$cache" "cache-only DNS" \
	|| fail "DnsCache did not enter cache-only mode"
send_query_expect_answer "Cached Query" "10.20.0.11"
send_query_expect_error "Uncached Query" "no upstream attached"
add_link_by_port_name "$cache" right DNS "$fallback" left DNS >/dev/null
wait_node_label "$cache" "caching DNS" \
	|| fail "DnsCache did not recover after its upstream was restored"
send_query_expect_answer "Uncached Query" "10.20.0.12"

# Removing the preferred fallback answer exposes the later answer. Restoring
# the same numbered slot restores its priority.
fallback_primary="$(link_id_by_port_names "Fallback DNS" "slot 2" "Primary DNS" DNS)"
cli graph delete-link --id "$fallback_primary" >/dev/null
wait_node_label "$fallback" "fallback across 2 DNS backends" \
	|| fail "DnsFallback did not detach its primary backend"
send_query_expect_answer "Fallback Query" "10.20.0.2"
add_link_by_port_name "$fallback" right "slot 2" "$primary" left DNS >/dev/null
wait_node_label "$fallback" "fallback across 3 DNS backends" \
	|| fail "DnsFallback did not restore its primary backend"
send_query_expect_answer "Fallback Query" "10.20.0.1"

# A timeout edit replaces the fallback runtime. Existing input and output links
# must converge on the replacement.
cli graph apply-menu --name "Fallback DNS" \
	--json '{"config":{"attemptTimeoutMS":25}}' >/dev/null
wait_node_label "$fallback" "fallback across 3 DNS backends" \
	|| fail "DnsFallback lost backends after its timeout edit"
send_query_expect_answer "Fallback Query" "10.20.0.1"

# A router backend detach affects only requests routed to that backend.
router_secondary="$(link_id_by_port_names "DNS Router" "slot 2" "Secondary DNS" DNS)"
cli graph delete-link --id "$router_secondary" >/dev/null
wait_node_label "$router" "routing DNS" \
	|| fail "DnsRouter did not detach its second backend"
send_query_expect_answer "Router One Query" "10.20.0.21"
send_query_expect_error "Router Two Query" "no upstream attached"
add_link_by_port_name "$router" right "slot 2" "$secondary" left DNS >/dev/null
wait_node_label "$router" "routing 2 DNS backends" \
	|| fail "DnsRouter did not restore its second backend"
send_query_expect_answer "Router Two Query" "10.20.0.32"

# Invalid rules must not replace the last valid route.
apply_router_rules $'THIS IS NOT A ROUTER RULE'
wait_node_label "$router" "invalid config" \
	|| fail "DnsRouter did not report invalid rules"
send_query_expect_answer "Router One Query" "10.20.0.21"
send_query_expect_answer "Router Two Query" "10.20.0.32"

# Move the special route from slot 2 to a new slot 3 and attach the provider
# without rebuilding the router node.
slot_three_rules=$'ADDR_S router-one.lifecycle.test.\nBACKEND slot 3\n\nTRUE\nBACKEND slot 1'
apply_router_rules "$slot_three_rules"
retry "$ALMAGEST_E2E_TIMEOUT" "DnsRouter slot 3 port" \
	port_exists "DNS Router" right "slot 3" \
	|| fail "DnsRouter did not create slot 3 for its new rules"
add_link_by_port_name "$router" right "slot 3" "$secondary" left DNS >/dev/null
wait_node_label "$router" "routing 3 DNS backends" \
	|| fail "DnsRouter did not attach its slot 3 backend"
send_query_expect_answer "Router One Query" "10.20.0.31"
send_query_expect_answer "Router Two Query" "10.20.0.22"

# A query must discard its resolver after link removal and accept the current
# router again after the link is restored.
router_query="$(link_id_by_port_names "Router One Query" DNS "DNS Router" DNS)"
cli graph delete-link --id "$router_query" >/dev/null
send_query_expect_error "Router One Query" "dns is not ready"
add_link_by_port_name "$router_one_query" right DNS "$router" left DNS >/dev/null
send_query_expect_answer "Router One Query" "10.20.0.31"

# Slot 2 is no longer used by the rules. Once its link is removed, the dynamic
# port must also disappear.
router_secondary="$(link_id_by_port_names "DNS Router" "slot 2" "Secondary DNS" DNS)"
cli graph delete-link --id "$router_secondary" >/dev/null
send_query_expect_answer "Router One Query" "10.20.0.31"
send_query_expect_answer "Router Two Query" "10.20.0.22"

retry "$ALMAGEST_E2E_TIMEOUT" "unused DnsRouter slot 2 removal" \
	port_missing "DNS Router" right "slot 2" \
	|| fail "DnsRouter kept unused slot 2 after its rule and link were removed"

log "case passed"
