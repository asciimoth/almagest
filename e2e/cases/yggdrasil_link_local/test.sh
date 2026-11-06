#!/usr/bin/env bash
set -euo pipefail
# shellcheck disable=SC1091 # The helper is supplied by the E2E image.
source /e2e/lib/common.sh

menu_field() {
	local node="$1"
	local block="$2"
	local field="$3"
	cli graph menu --name "$node" | jq -er --arg block "$block" --arg field "$field" '
		.blocks[] | select(.id == $block) | .items[] | select(.id == $field) | (.value // .text // "")
	'
}

peer_count_at_least() {
	local node="$1"
	local wanted="$2"
	local count
	count="$(menu_field "$node" status connectedPeers)" || return 1
	[[ "$count" =~ ^[0-9]+$ ]] && ((count >= wanted))
}

link_local_interface_ready() {
	local node="$1"
	[[ "$(menu_field "$node" status linkLocalInterfaces)" == "1" ]]
}

http_succeeded() {
	local previous_count="$1"
	local menu count
	menu="$(cli graph menu --name "HTTP Client")" || return 1
	jq -e '
		[.blocks[] | select(.id == "result") | .items[] | select(.id == "status" or .id == "error" or .id == "response") | {(.id): (.value // .text // "")}] | add
		| .status == "Status: 200 OK" and .error == "" and .response == "yggdrasil link local"
	' <<<"$menu" >/dev/null || return 1
	count="$(server_request_count)" || return 1
	((count > previous_count))
}

server_request_count() {
	cli log subscribe | jq -er '[.entries[] | select(.node_class == "almagest/HttpServer" and (.message | contains("GET /")))] | length'
}

assert_http_success() {
	local description="$1"
	local previous_count
	previous_count="$(server_request_count)" || fail "could not read server request count"
	cli graph click-menu-button --name "HTTP Client" --block request --button send >/dev/null
	retry "$ALMAGEST_E2E_TIMEOUT" "$description" http_succeeded "$previous_count" || fail "$description failed"
}

trap cleanup_daemon EXIT
start_daemon
wait_graph_node "Ygg A" || fail "first Yggdrasil node missing"
wait_graph_node "Ygg B" || fail "second Yggdrasil node missing"
ygg_a="$(node_id_by_name "Ygg A")"
ygg_b="$(node_id_by_name "Ygg B")"
out_network="$(node_id_by_name "OutNetwork")"
wait_node_label "$ygg_a" up || fail "first Yggdrasil node did not start"
wait_node_label "$ygg_b" up || fail "second Yggdrasil node did not start"
retry "$ALMAGEST_E2E_TIMEOUT" "first link-local listener" link_local_interface_ready "Ygg A" || fail "first link-local listener did not start"
retry "$ALMAGEST_E2E_TIMEOUT" "second link-local listener" link_local_interface_ready "Ygg B" || fail "second link-local listener did not start"
retry "$ALMAGEST_E2E_TIMEOUT" "first discovered peer" peer_count_at_least "Ygg A" 1 || fail "first node did not discover its peer"
retry "$ALMAGEST_E2E_TIMEOUT" "second discovered peer" peer_count_at_least "Ygg B" 1 || fail "second node did not discover its peer"
assert_http_success "HTTP through discovered peers"

network_link="$(link_id_by_port_names "Ygg A" default OutNetwork Network)"
cli graph delete-link --id "$network_link" >/dev/null
wait_node_label "$ygg_a" "waiting for network" || fail "first node kept its detached network"
add_link_by_port_name "$ygg_a" right default "$out_network" left Network >/dev/null
wait_node_label "$ygg_a" up || fail "first node did not accept its network again"
retry "$ALMAGEST_E2E_TIMEOUT" "rediscovered peer" peer_count_at_least "Ygg A" 1 || fail "first node did not rediscover its peer"
assert_http_success "HTTP after network reattach"
log "case passed"
