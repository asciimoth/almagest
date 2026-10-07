#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

export ALMAGEST_E2E_TMP_FILE_STORAGE=0
export ALMAGEST_E2E_FILE_STORAGE=/var/lib/almagest-e2e-lib-control-plane

watch_pid=""
watch_output="$E2E_RUNTIME/engine-watch.json"
watch_error="$E2E_RUNTIME/engine-watch.err"

menu_value() {
	local node="$1"
	local block="$2"
	local item="$3"
	cli graph menu --id "$node" | jq -er \
		--arg block "$block" \
		--arg item "$item" \
		'
		.blocks[]
		| select(.id == $block)
		| .items[]
		| select(.id == $item)
		| (.value // .text // "")
		'
}

http_success_is() {
	local node="$1"
	[[ "$(menu_value "$node" result status)" == "Status: 200 OK" \
		&& -z "$(menu_value "$node" result error)" \
		&& "$(menu_value "$node" result response)" == "control-plane-alive" ]]
}

assert_http_success_once() {
	local node="$1"
	local description="$2"
	cli graph click-menu-button --id "$node" --block request --button send >/dev/null
	retry "$ALMAGEST_E2E_TIMEOUT" "$description" http_success_is "$node" \
		|| fail "$description did not return the expected response"
}

kv_update_field() {
	local node="$1"
	local field="$2"
	local value="$3"
	local message
	jq . <<<"$value" >/dev/null \
		|| fail "DebugKV field $field received invalid JSON value: $value"
	message="$(jq -nc \
		--arg menu "NODE${node}MENU" \
		--arg field "$field" \
		--argjson value "$value" \
		'{
			type: "field.update",
			menuId: $menu,
			field: {blockId: "entry", fieldId: $field},
			value: $value
		}')"
	cli engine send-node-formular-msg --id "$node" --message "$message" >/dev/null
}

kv_press() {
	local node="$1"
	local button="$2"
	cli graph click-menu-button --id "$node" --block entry --button "$button" >/dev/null
}

kv_status_is() {
	local node="$1"
	local status="$2"
	local error="$3"
	[[ "$(menu_value "$node" result status)" == "Status: $status" \
		&& "$(menu_value "$node" result error)" == "$error" ]]
}

wait_kv_status() {
	local node="$1"
	local status="$2"
	local error="$3"
	local description="$4"
	retry "$ALMAGEST_E2E_TIMEOUT" "$description" \
		kv_status_is "$node" "$status" "$error" \
		|| fail "$description did not produce status $status and error $error"
}

configure_kv_entry() {
	local node="$1"
	local store="$2"
	local plugin="$3"
	local path="$4"
	local key="$5"
	local value="$6"
	local string_value="$7"
	local hidden="$8"
	kv_update_field "$node" store "$(jq -Rn --arg value "$store" '$value')"
	kv_update_field "$node" plugin "$(jq -Rn --arg value "$plugin" '$value')"
	kv_update_field "$node" path "$(jq -Rn --arg value "$path" '$value')"
	kv_update_field "$node" key "$(jq -Rn --arg value "$key" '$value')"
	kv_update_field "$node" value "$(jq -Rn --arg value "$value" '$value')"
	kv_update_field "$node" string "$string_value"
	kv_update_field "$node" hidden "$hidden"
}

node_is_error_placeholder() {
	cli graph node --id "$1" | jq -e \
		'.snapshot.placeholder and any(.snapshot.popups[]; .type == "err")' >/dev/null
}

node_is_live() {
	cli graph node --id "$1" | jq -e '(.snapshot.placeholder | not)' >/dev/null
}

worker_absent() {
	local node="$1"
	cli engine workers-snapshot | jq -e --argjson node "$node" \
		'([.workers[] | select(.node == $node)] | length) == 0' >/dev/null
}

watch_has_initial_state() {
	jq -s -e '
		any(.[]; .payload.notification.kind == "colormap")
		and any(.[];
			.payload.notification.kind == "workspace"
			and .payload.notification.workspace_notification.kind == "workspace_snapshot")
		and any(.[];
			.payload.notification.kind == "worker"
			and .payload.notification.worker_notification.kind == "worker_snapshot")
	' "$watch_output" >/dev/null 2>&1
}

watch_has_worker_event() {
	local kind="$1"
	local node="$2"
	jq -s -e --arg kind "$kind" --argjson node "$node" '
		any(.[];
			.payload.notification.kind == "worker"
			and .payload.notification.worker_notification.kind == $kind
			and .payload.notification.worker_notification.worker.node == $node)
	' "$watch_output" >/dev/null 2>&1
}

watch_has_placeholder_event() {
	local node="$1"
	jq -s -e --argjson node "$node" '
		any(.[];
			.payload.notification.kind == "workspace"
			and .payload.notification.workspace_notification.kind == "node_updated"
			and .payload.notification.workspace_notification.id == $node
			and .payload.notification.workspace_notification.node.placeholder)
	' "$watch_output" >/dev/null 2>&1
}

node_exists() {
	cli graph node --id "$1" >/dev/null 2>&1
}

node_missing() {
	! node_exists "$1"
}

class_node_count_is() {
	local class="$1"
	local expected="$2"
	[[ "$(cli engine workspace-snapshot | jq -r --arg class "$class" \
		'[.snapshot.nodes[] | select(.class == $class)] | length')" == "$expected" ]]
}

start_daemon

for name in \
	"Control Network" "Control Server" "Control Client" \
	"KV Writer" "KV Reader" "Worker Failure" "Callback Failure"; do
	wait_graph_node "$name" || fail "$name node is missing"
done

server="$(node_id_by_name "Control Server")"
client="$(node_id_by_name "Control Client")"
writer="$(node_id_by_name "KV Writer")"
reader="$(node_id_by_name "KV Reader")"
worker_failure="$(node_id_by_name "Worker Failure")"
callback_failure="$(node_id_by_name "Callback Failure")"

wait_node_label "$server" "127.0.0.1:18080" \
	|| fail "control HTTP server did not listen"
wait_node_label "$client" ready || fail "control HTTP client did not become ready"
assert_http_success_once "$client" "initial control HTTP request"

# Exercise direct engine snapshot wrappers on current classes and graph data.
cli engine node-class-snapshot --class almagest/DebugKV | \
	jq -e '.snapshot.class == "almagest/DebugKV"' >/dev/null \
	|| fail "DebugKV class snapshot is incorrect"
cli engine node-class-long-description --class almagest/DebugKV | \
	jq -e '.description | contains("KV storage")' >/dev/null \
	|| fail "DebugKV class description is incorrect"
server_port="$(node_port_id "$server" right Network)"
server_link="$(link_id_by_port_names "Control Server" Network "Control Network" Network)"
cli engine port-snapshot --id "$server_port" | \
	jq -e --argjson node "$server" '.snapshot.node == $node and .snapshot.name == "Network"' >/dev/null \
	|| fail "server port snapshot is incorrect"
cli engine link-snapshot --id "$server_link" | \
	jq -e --argjson node "$server" \
		'.snapshot.right_port_node == $node or .snapshot.left_port_node == $node' >/dev/null \
	|| fail "server link snapshot is incorrect"

# Start a real notification subscriber before the mutations below.
rm -f "$watch_output" "$watch_error"
almagest-cli --url "unix://$E2E_ADMIN_SOCK" engine watch --full-snapshot \
	>"$watch_output" 2>"$watch_error" &
watch_pid="$!"
retry "$ALMAGEST_E2E_TIMEOUT" "initial engine notification state" \
	watch_has_initial_state \
	|| fail "engine watch did not publish its initial state"

# Use two node instances against one real KV manager. This proves that node
# state and persisted storage state are not being confused.
configure_kv_entry "$writer" default "" '["control","plain"]' shared first-value true false
kv_press "$writer" set
wait_kv_status "$writer" created "" "default KV creation"
cli kv get --path control/plain --key shared | \
	jq -e '.found and .value.data == "first-value" and .value.string and (.value.hidden | not)' >/dev/null \
	|| fail "node-created default KV value did not reach the admin API"

configure_kv_entry "$reader" default "" '["control","plain"]' shared ignored true false
kv_press "$reader" read
wait_kv_status "$reader" read "" "default KV read from second node"
[[ "$(menu_value "$reader" entry value)" == "first-value" ]] \
	|| fail "second DebugKV node read an unexpected default-store value"
kv_update_field "$reader" value '"second-value"'
kv_press "$reader" set
wait_kv_status "$reader" updated "" "default KV update from second node"
cli kv get --path control/plain --key shared | \
	jq -e '.found and .value.data == "second-value"' >/dev/null \
	|| fail "second DebugKV node did not update the shared value"
kv_press "$reader" delete
wait_kv_status "$reader" deleted "" "default KV deletion"
cli kv get --path control/plain --key shared | jq -e '(.found | not)' >/dev/null \
	|| fail "DebugKV delete left the default-store value behind"
kv_press "$reader" delete
wait_kv_status "$reader" "not found" "" "idempotent missing KV deletion"

configure_kv_entry "$writer" plugin control-plugin '["nested","binary"]' blob AAH/ false true
kv_press "$writer" set
wait_kv_status "$writer" created "" "plugin binary KV creation"
cli kv get --plugin control-plugin --path nested/binary --key blob | \
	jq -e '
		.found and .value.data == "AAH/" and .value.encoding == "base64"
		and (.value.string | not) and .value.hidden
	' >/dev/null || fail "hidden binary value did not round-trip through the admin API"
cli kv get --path nested/binary --key blob | jq -e '(.found | not)' >/dev/null \
	|| fail "plugin KV value leaked into the default store"
cli kv get --plugin control-plugin --path control/plain --key shared | \
	jq -e '(.found | not)' >/dev/null \
	|| fail "default KV path leaked into the plugin store"

# Invalid input must report a local error and retain the previous stored data.
kv_update_field "$writer" value '"***"'
kv_press "$writer" set
wait_kv_status "$writer" "set failed" "value is not valid base64" \
	"invalid base64 rejection"
cli kv get --plugin control-plugin --path nested/binary --key blob | \
	jq -e '.found and .value.data == "AAH/"' >/dev/null \
	|| fail "invalid base64 replaced the stored binary value"

kv_update_field "$writer" path '"not-json"'
kv_press "$writer" read
wait_kv_status "$writer" "read failed" "path must be a JSON array of strings" \
	"invalid path rejection"
kv_update_field "$writer" path '"[\"nested\",\"binary\"]"'
kv_update_field "$writer" plugin '""'
kv_press "$writer" read
wait_kv_status "$writer" "read failed" "plugin name is required for a plugin store" \
	"missing plugin rejection"

# Worker failure must publish its complete lifecycle and replace only its
# parent node. The failed worker must not remain in the diagnostic snapshot.
cli graph click-menu-button \
	--id "$worker_failure" --block actions --button panic-worker >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "debug worker spawn event" \
	watch_has_worker_event worker_spawned "$worker_failure" \
	|| fail "engine watch did not publish the worker spawn"
retry "$ALMAGEST_E2E_TIMEOUT" "debug worker failure event" \
	watch_has_worker_event worker_failed "$worker_failure" \
	|| fail "engine watch did not publish the worker failure"
retry "$ALMAGEST_E2E_TIMEOUT" "worker parent placeholder" \
	node_is_error_placeholder "$worker_failure" \
	|| fail "failed worker parent did not become an error placeholder"
retry "$ALMAGEST_E2E_TIMEOUT" "worker placeholder notification" \
	watch_has_placeholder_event "$worker_failure" \
	|| fail "engine watch did not publish the worker placeholder"
retry "$ALMAGEST_E2E_TIMEOUT" "failed worker removal" worker_absent "$worker_failure" \
	|| fail "failed worker remained in the worker snapshot"

# A callback panic follows the workspace recovery path without a worker event.
cli graph click-menu-button \
	--id "$callback_failure" --block actions --button panic >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "callback parent placeholder" \
	node_is_error_placeholder "$callback_failure" \
	|| fail "panicking callback parent did not become an error placeholder"
retry "$ALMAGEST_E2E_TIMEOUT" "callback placeholder notification" \
	watch_has_placeholder_event "$callback_failure" \
	|| fail "engine watch did not publish the callback placeholder"
assert_http_success_once "$client" "control request after independent failures"

kill "$watch_pid" 2>/dev/null || true
wait "$watch_pid" 2>/dev/null || true
watch_pid=""

# A restart reconstructs implementations from config while retaining storage.
stop_daemon
start_daemon
worker_failure="$(node_id_by_name "Worker Failure")"
callback_failure="$(node_id_by_name "Callback Failure")"
client="$(node_id_by_name "Control Client")"
retry "$ALMAGEST_E2E_TIMEOUT" "worker node restart recovery" \
	node_is_live "$worker_failure" \
	|| fail "worker failure node stayed a placeholder after restart"
retry "$ALMAGEST_E2E_TIMEOUT" "callback node restart recovery" \
	node_is_live "$callback_failure" \
	|| fail "callback failure node stayed a placeholder after restart"
cli graph menu --id "$worker_failure" | \
	jq -e 'any(.blocks[].items[]; .id == "panic-worker")' >/dev/null \
	|| fail "restarted worker failure node did not restore its menu"
cli kv get --plugin control-plugin --path nested/binary --key blob | \
	jq -e '.found and .value.data == "AAH/" and .value.hidden' >/dev/null \
	|| fail "binary KV value did not survive daemon restart"
assert_http_success_once "$client" "first control request after restart"

# Class-wide removal is one batch mutation with stable IDs.
cli engine remove-nodes-by-class --class almagest/DebugKV >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "KV writer batch removal" node_missing "$writer" \
	|| fail "KV writer survived class-wide removal"
retry "$ALMAGEST_E2E_TIMEOUT" "KV reader batch removal" node_missing "$reader" \
	|| fail "KV reader survived class-wide removal"
cli engine undo >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "class-wide removal undo" \
	class_node_count_is almagest/DebugKV 2 \
	|| fail "undo did not restore both KV nodes"
node_exists "$writer" || fail "undo did not restore the KV writer ID"
node_exists "$reader" || fail "undo did not restore the KV reader ID"
cli engine redo >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "class-wide removal redo" \
	class_node_count_is almagest/DebugKV 0 \
	|| fail "redo did not remove both KV nodes"
assert_http_success_once "$client" "control request after class history"

# Markdown is intentionally stateless, but each node still needs a distinct
# implementation identity. Exercise construction, clipboard reconstruction,
# batched history reconstruction, and daemon config reconstruction at scale.
markdown_ids=()
for i in $(seq 1 24); do
	markdown_ids+=("$(add_node almagest/Markdown "Markdown $i")")
done
retry "$ALMAGEST_E2E_TIMEOUT" "Markdown source node creation" \
	class_node_count_is almagest/Markdown 24 \
	|| fail "Markdown source creation did not create 24 nodes"
markdown_ids_json="$(printf '%s\n' "${markdown_ids[@]}" | jq -sc 'map(tonumber)')"
markdown_ids_csv="$(jq -r 'join(",")' <<<"$markdown_ids_json")"

markdown_clipboard="$(cli engine copy --ids "$markdown_ids_csv" | jq -er '.data')"
[[ -n "$markdown_clipboard" ]] || fail "Markdown copy returned an empty clipboard"
markdown_paste_result="$(cli engine paste --data "$markdown_clipboard")"
markdown_pasted_ids="$(jq -c '.ids' <<<"$markdown_paste_result")"
[[ "$(jq 'length' <<<"$markdown_pasted_ids")" == 24 ]] \
	|| fail "Markdown paste did not reconstruct all 24 nodes"
retry "$ALMAGEST_E2E_TIMEOUT" "Markdown pasted node creation" \
	class_node_count_is almagest/Markdown 48 \
	|| fail "Markdown paste did not leave 48 nodes"
markdown_all_ids="$(jq -cn \
	--argjson source "$markdown_ids_json" \
	--argjson pasted "$markdown_pasted_ids" \
	'$source + $pasted')"
cli engine workspace-snapshot | jq -e --argjson ids "$markdown_all_ids" '
	.snapshot.nodes as $nodes
	| all($ids[];
		. as $id
		| $nodes[($id | tostring)].class == "almagest/Markdown"
		and ($nodes[($id | tostring)].placeholder | not))
' >/dev/null || fail "Markdown instances do not all have distinct live records"

cli graph delete-nodes --ids "$markdown_ids_csv" >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "Markdown batch removal" \
	class_node_count_is almagest/Markdown 24 \
	|| fail "Markdown batch removal did not remove all source nodes"
cli engine undo >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "Markdown batch undo" \
	class_node_count_is almagest/Markdown 48 \
	|| fail "Markdown batch undo did not reconstruct every source node"
cli engine redo >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "Markdown batch redo" \
	class_node_count_is almagest/Markdown 24 \
	|| fail "Markdown batch redo did not remove every source node"
cli engine undo >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "second Markdown batch undo" \
	class_node_count_is almagest/Markdown 48 \
	|| fail "second Markdown batch undo did not reconstruct every source node"
cli engine workspace-snapshot | jq -e --argjson ids "$markdown_all_ids" '
	.snapshot.nodes as $nodes
	| all($ids[];
		. as $id
		| $nodes[($id | tostring)].class == "almagest/Markdown"
		and ($nodes[($id | tostring)].placeholder | not))
' >/dev/null || fail "Markdown undo did not preserve live node IDs"

stop_daemon
start_daemon
retry "$ALMAGEST_E2E_TIMEOUT" "Markdown restart reconstruction" \
	class_node_count_is almagest/Markdown 48 \
	|| fail "daemon restart did not reconstruct every Markdown node"
cli engine workspace-snapshot | jq -e '
	[.snapshot.nodes[] | select(.class == "almagest/Markdown")] as $nodes
	| ($nodes | length) == 48
	and all($nodes[]; .placeholder | not)
	and ([$nodes[].name] | unique | length) == 48
' >/dev/null || fail "Markdown nodes lost names or became placeholders after restart"
cli engine remove-nodes-by-class --class almagest/Markdown >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "Markdown cleanup" \
	class_node_count_is almagest/Markdown 0 \
	|| fail "Markdown cleanup left nodes behind"

log "case passed"
