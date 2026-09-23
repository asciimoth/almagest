#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

menu_value() {
	local node="$1"
	local block="$2"
	local field="$3"
	cli graph menu --id "$node" | jq -er \
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

http_success_is() {
	local node="$1"
	[[ "$(menu_value "$node" result status)" == "Status: 200 OK" \
		&& -z "$(menu_value "$node" result error)" \
		&& "$(menu_value "$node" result response)" == "history-runtime-response" ]]
}

http_failure_is() {
	local node="$1"
	local status
	local error
	status="$(menu_value "$node" result status)" || return 1
	error="$(menu_value "$node" result error)" || return 1
	[[ "$status" == "Status: fail" && -n "$error" ]]
}

click_http_once() {
	local node="$1"
	cli graph click-menu-button \
		--id "$node" \
		--block request \
		--button send \
		>/dev/null
}

assert_http_success_once() {
	local node="$1"
	local description="$2"
	click_http_once "$node"
	retry "$ALMAGEST_E2E_TIMEOUT" "$description" http_success_is "$node" \
		|| fail "$description did not return the expected response"
}

assert_http_failure_once() {
	local node="$1"
	local description="$2"
	click_http_once "$node"
	retry "$ALMAGEST_E2E_TIMEOUT" "$description" http_failure_is "$node" \
		|| fail "$description did not report a request failure"
}

node_exists() {
	cli graph node --id "$1" >/dev/null 2>&1
}

node_missing() {
	! node_exists "$1"
}

node_name_is() {
	[[ "$(cli graph node --id "$1" | jq -er '.snapshot.name')" == "$2" ]]
}

node_position_is() {
	[[ "$(cli graph node --id "$1" | jq -er '.snapshot.position')" == "$2" ]]
}

link_id_by_node_ids() {
	local right_node="$1"
	local right_port="$2"
	local left_node="$3"
	local left_port="$4"
	cli engine workspace-snapshot | jq -er \
		--arg right_node "$right_node" \
		--arg right_port "$right_port" \
		--arg left_node "$left_node" \
		--arg left_port "$left_port" \
		'
		.snapshot as $workspace
		| $workspace.links
		| to_entries[]
		| select(
			(.value.right_port_node | tostring) == $right_node
			and (.value.left_port_node | tostring) == $left_node
			and $workspace.ports[(.value.right_port | tostring)].name == $right_port
			and $workspace.ports[(.value.left_port | tostring)].name == $left_port
		)
		| .key
		'
}

link_exists_by_node_ids() {
	link_id_by_node_ids "$@" >/dev/null
}

node_has_no_links() {
	local node="$1"
	cli engine workspace-snapshot | jq -e \
		--arg node "$node" \
		'
		[
			.snapshot.links[]
			| select(
				(.right_port_node | tostring) == $node
				or (.left_port_node | tostring) == $node
			)
		] | length == 0
		' >/dev/null
}

pasted_id_by_class() {
	local ids_json="$1"
	local class="$2"
	local id
	while IFS= read -r id; do
		if [[ "$(cli graph node --id "$id" | jq -er '.snapshot.class')" == "$class" ]]; then
			printf '%s\n' "$id"
			return 0
		fi
	done < <(jq -r '.[]' <<<"$ids_json")
	return 1
}

start_daemon

network="$(node_id_by_name "History Network")"
server="$(node_id_by_name "History Server")"
client="$(node_id_by_name "History Client")"

wait_node_label "$server" "127.0.0.1:8080" || fail "initial HTTP server did not listen"
wait_node_label "$client" ready || fail "initial HTTP client did not become ready"

# The first request after startup must work without a request retry.
assert_http_success_once "$client" "initial HTTP request"

# Exercise the same live link across remove, undo, redo, and a second undo.
client_link="$(link_id_by_node_ids "$client" Network "$network" Network)"
cli graph delete-link --id "$client_link" >/dev/null
assert_http_failure_once "$client" "request after network-link removal"

cli engine undo >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "client link restored by undo" \
	link_exists_by_node_ids "$client" Network "$network" Network \
	|| fail "undo did not restore the client network link"
assert_http_success_once "$client" "first request after link undo"

cli engine redo >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "client link removed by redo" \
	node_has_no_links "$client" \
	|| fail "redo did not remove the client network link"
assert_http_failure_once "$client" "request after link redo"

cli engine undo >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "client link restored by second undo" \
	link_exists_by_node_ids "$client" Network "$network" Network \
	|| fail "second undo did not restore the client network link"
assert_http_success_once "$client" "first request after second link undo"

# Copy and paste the complete runtime graph. Its internal links must refer to
# the pasted network, not to the original network.
clipboard="$(cli engine copy --ids "$network,$server,$client" | jq -er '.data')"
[[ -n "$clipboard" ]] || fail "engine copy returned an empty clipboard"
paste_result="$(cli engine paste --data "$clipboard")"
pasted_ids="$(jq -c '.ids' <<<"$paste_result")"
[[ "$(jq 'length' <<<"$pasted_ids")" == 3 ]] \
	|| fail "complete graph paste did not create three nodes"

pasted_network="$(pasted_id_by_class "$pasted_ids" almagest/MockNetwork)"
pasted_server="$(pasted_id_by_class "$pasted_ids" almagest/HttpServer)"
pasted_client="$(pasted_id_by_class "$pasted_ids" almagest/HttpClient)"
wait_node_label "$pasted_server" "127.0.0.1:8080" \
	|| fail "pasted HTTP server did not listen"
wait_node_label "$pasted_client" ready || fail "pasted HTTP client did not become ready"
link_exists_by_node_ids "$pasted_client" Network "$pasted_network" Network \
	|| fail "pasted client link does not use the pasted network"
link_exists_by_node_ids "$pasted_server" Network "$pasted_network" Network \
	|| fail "pasted server link does not use the pasted network"
assert_http_success_once "$pasted_client" "pasted graph HTTP request"

# History on the pasted link must close and replace its runtime wrapper.
pasted_client_link="$(link_id_by_node_ids "$pasted_client" Network "$pasted_network" Network)"
cli graph delete-link --id "$pasted_client_link" >/dev/null
assert_http_failure_once "$pasted_client" "pasted request after link removal"
cli engine undo >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "pasted client link restored by undo" \
	link_exists_by_node_ids "$pasted_client" Network "$pasted_network" Network \
	|| fail "undo did not restore the pasted client link"
assert_http_success_once "$pasted_client" "first pasted request after link undo"

# Deleting and restoring the server exercises worker shutdown, listener
# release, and reconstruction on the same virtual address.
cli graph delete-node --id "$pasted_server" >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "pasted server removal" node_missing "$pasted_server" \
	|| fail "pasted server was not removed"
assert_http_failure_once "$pasted_client" "pasted request without a server"
cli engine undo >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "pasted server restored by undo" node_exists "$pasted_server" \
	|| fail "undo did not restore the pasted server"
wait_node_label "$pasted_server" "127.0.0.1:8080" \
	|| fail "restored pasted server did not listen"
assert_http_success_once "$pasted_client" "first request after server undo"
cli engine redo >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "pasted server removed by redo" node_missing "$pasted_server" \
	|| fail "redo did not remove the pasted server"
cli engine undo >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "pasted server restored by second undo" node_exists "$pasted_server" \
	|| fail "second undo did not restore the pasted server"
wait_node_label "$pasted_server" "127.0.0.1:8080" \
	|| fail "second restored pasted server did not listen"
assert_http_success_once "$pasted_client" "request after repeated server history"

# A partial clipboard must not retain a link to a node outside the selection.
partial_clipboard="$(cli engine copy --ids "$client" | jq -er '.data')"
partial_result="$(cli engine paste --data "$partial_clipboard")"
partial_client="$(jq -er '.ids | if length == 1 then .[0] else error("want one pasted node") end' <<<"$partial_result")"
retry "$ALMAGEST_E2E_TIMEOUT" "partial pasted client" node_exists "$partial_client" \
	|| fail "partial client paste did not create a node"
node_has_no_links "$partial_client" \
	|| fail "partial client paste retained an external network link"
assert_http_failure_once "$partial_client" "partial pasted client request"

cli engine undo >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "partial paste removed by undo" node_missing "$partial_client" \
	|| fail "undo did not remove the partial paste"
cli engine redo >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "partial paste restored by redo" node_exists "$partial_client" \
	|| fail "redo did not restore the partial paste"
node_has_no_links "$partial_client" \
	|| fail "redo restored an external link on the partial paste"
assert_http_failure_once "$partial_client" "partial pasted client request after redo"

# Renames are persistent metadata changes rather than history entries. A
# position is undoable. Its undo and redo must keep the rename and must not
# rebuild or interrupt the original data plane.
cli engine set-node-name --id "$server" --new-name "History Server Renamed" >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "server rename" \
	node_name_is "$server" "History Server Renamed" \
	|| fail "server rename was not applied"
cli engine set-node-position --id "$server" --position "120 240" >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "server position update" \
	node_position_is "$server" "120 240" \
	|| fail "server position was not applied"

cli engine undo >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "position undo" node_position_is "$server" "" \
	|| fail "position undo changed the wrong history entry"
node_name_is "$server" "History Server Renamed" \
	|| fail "position undo also reverted the server name"
cli engine redo >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "position redo" node_position_is "$server" "120 240" \
	|| fail "position redo did not restore the new position"
node_name_is "$server" "History Server Renamed" \
	|| fail "position redo changed the server name"
assert_http_success_once "$client" "original request after metadata history"

# A batched deletion must record one history entry with all incident links.
# Keeping the client alive while its network and server are absent also checks
# that undo installs a current wrapper instead of reviving a closed one.
cli graph delete-nodes --ids "$network,$server" >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "batched network removal" node_missing "$network" \
	|| fail "batched deletion did not remove the original network"
retry "$ALMAGEST_E2E_TIMEOUT" "batched server removal" node_missing "$server" \
	|| fail "batched deletion did not remove the original server"
assert_http_failure_once "$client" "request after batched runtime deletion"

cli engine undo >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "batched network undo" node_exists "$network" \
	|| fail "undo did not restore the batched network"
retry "$ALMAGEST_E2E_TIMEOUT" "batched server undo" node_exists "$server" \
	|| fail "undo did not restore the batched server"
retry "$ALMAGEST_E2E_TIMEOUT" "client link after batched undo" \
	link_exists_by_node_ids "$client" Network "$network" Network \
	|| fail "batched undo did not restore the client link"
retry "$ALMAGEST_E2E_TIMEOUT" "server link after batched undo" \
	link_exists_by_node_ids "$server" Network "$network" Network \
	|| fail "batched undo did not restore the server link"
wait_node_label "$server" "127.0.0.1:8080" \
	|| fail "batched undo did not restart the server"
assert_http_success_once "$client" "first request after batched runtime undo"

cli engine redo >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "batched network redo" node_missing "$network" \
	|| fail "redo did not remove the batched network"
retry "$ALMAGEST_E2E_TIMEOUT" "batched server redo" node_missing "$server" \
	|| fail "redo did not remove the batched server"
assert_http_failure_once "$client" "request after batched runtime redo"
cli engine undo >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "second batched network undo" node_exists "$network" \
	|| fail "second undo did not restore the batched network"
retry "$ALMAGEST_E2E_TIMEOUT" "second batched server undo" node_exists "$server" \
	|| fail "second undo did not restore the batched server"
wait_node_label "$server" "127.0.0.1:8080" \
	|| fail "second batched undo did not restart the server"
assert_http_success_once "$client" "request after repeated batched history"

log "case passed"
