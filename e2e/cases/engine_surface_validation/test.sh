#!/usr/bin/env bash
set -euo pipefail

: "${ALMAGEST_E2E_LINK_MATRIX_ATTEMPTS:=100}"
: "${ALMAGEST_E2E_MENU_JOBS:=4}"
source /e2e/lib/common.sh

if [[ ! "$ALMAGEST_E2E_LINK_MATRIX_ATTEMPTS" =~ ^[0-9]+$ ]]; then
	fail "ALMAGEST_E2E_LINK_MATRIX_ATTEMPTS must be a non-negative integer"
fi
if [[ ! "$ALMAGEST_E2E_MENU_JOBS" =~ ^[1-9][0-9]*$ ]]; then
	fail "ALMAGEST_E2E_MENU_JOBS must be a positive integer"
fi

raw_engine() {
	local method="$1"
	local payload="$2"
	cli raw --surface engine --method "$method" --payload "$payload"
}

expect_raw_failure() {
	local description="$1"
	local method="$2"
	local payload="$3"
	local output="$E2E_RUNTIME/${description}.out"
	local error="$E2E_RUNTIME/${description}.err"

	if raw_engine "$method" "$payload" >"$output" 2>"$error"; then
		fail "$description unexpectedly succeeded"
	fi
}

exercise_menu() {
	local class="$1"
	local node="$2"
	local menu="$E2E_RUNTIME/menu-${node}.json"
	local desired="$E2E_RUNTIME/menu-${node}-desired.json"
	local item
	local message

	case "$class" in
		almagest/Amnesia | almagest/AmnesiaCtrl | almagest/ErrorsWorkers | \
		almagest/DebugKV | almagest/UDPPingClient | almagest/UDPPingServer | \
		almagest/DnsCache | almagest/DnsClient | almagest/DnsFallback | \
		almagest/DnsQuery | almagest/DnsRouter | almagest/DnsServer | \
		almagest/MeshNames | almagest/StaticDns | almagest/Tun2Dns | \
		almagest/FetchAddr | almagest/HttpClient | almagest/HttpServer | \
		almagest/NetworkFirewall | almagest/MockNetwork | almagest/NetworkRouter | \
		almagest/Remapper | almagest/Sniffer | almagest/Spoofer | \
		almagest/LocalProxy | almagest/SocksClient | almagest/SocksServer | \
		almagest/TailscaleCtrl | almagest/TlsWrapper | almagest/TlsMitm | \
		almagest/TlsTerminator | almagest/TimeTrigger | almagest/DefaultTun | \
		almagest/Tun | almagest/TunFirewall | almagest/TunSplitter | \
		almagest/VTun | almagest/WGConfig | \
		almagest/WireGuard)
			;;
		*)
			return
			;;
	esac

	cli graph menu --id "$node" >"$menu" \
		|| fail "$class did not publish its menu"

	# Send type-correct boundary and malformed candidates to fields that ask for
	# validation. Validation messages do not apply state, so external resources
	# remain unchanged. The published value is sent last to clear any error.
	while IFS= read -r item; do
		while IFS= read -r candidate; do
			message="$(jq -nc \
				--arg menu "NODE${node}MENU" \
				--argjson item "$item" \
				--argjson value "$candidate" '
				{
					type: "field.validate",
					menuId: $menu,
					field: {blockId: $item.block, fieldId: $item.field},
					value: $value
				}')"
			cli engine send-node-formular-msg --id "$node" --message "$message" >/dev/null
		done < <(jq -c '
			if .kind == "text" then
				"", "not a valid value []://", ("x" * 300)
			elif .kind == "int" then
				-1, 0, 65536, 2147483648
			elif .kind == "checkbox" then
				true, false
			else
				null, "not a valid value", -1, []
			end
		' <<<"$item")
		message="$(jq -nc --arg menu "NODE${node}MENU" --argjson item "$item" '
			{
				type: "field.validate",
				menuId: $menu,
				field: {blockId: $item.block, fieldId: $item.field},
				value: $item.value
			}')"
		cli engine send-node-formular-msg --id "$node" --message "$message" >/dev/null
	done < <(jq -c '
		.blocks[] as $block
		| $block.items[]
		| select(.validate == true and has("value"))
		| {block: $block.id, field: .id, kind: .kind, value: .value}
	' "$menu")

	# Array fields publish their validation rules in template items instead of
	# top-level values. Exercise those nested validators against a draft element.
	while IFS= read -r item; do
		for candidate in '""' '"not a valid value []://"' '-1' '65536' '[]'; do
			message="$(jq -nc \
				--arg menu "NODE${node}MENU" \
				--argjson item "$item" \
				--argjson value "$candidate" '
				{
					type: "field.validate",
					menuId: $menu,
					field: {
						blockId: $item.block,
						fieldId: $item.field,
						elementPath: [{arrayFieldId: $item.array, elementId: "surface-draft"}]
					},
					value: $value
				}')"
			cli engine send-node-formular-msg --id "$node" --message "$message" >/dev/null
		done
	done < <(jq -c '
		.blocks[] as $block
		| $block.items[]
		| select(.kind == "array") as $array
		| $array.templates[]?.items[]
		| select(.validate == true)
		| {block: $block.id, array: $array.id, field: .id, kind: .kind}
	' "$menu")

	# Autocomplete is also a distinct formular message path. Request hints for
	# all declared top-level and array-template completion fields.
	while IFS= read -r field; do
		message="$(jq -nc --arg menu "NODE${node}MENU" --argjson field "$field" '
			{
				type: "autocomplete.request",
				menuId: $menu,
				field: ({blockId: $field.block, fieldId: $field.field}
					+ if $field.array == null then {} else {
						elementPath: [{arrayFieldId: $field.array, elementId: "surface-draft"}]
					} end),
				prefix: ""
			}')"
		cli engine send-node-formular-msg --id "$node" --message "$message" >/dev/null
	done < <(jq -c '
		.blocks[] as $block
		| $block.items[]
		| if .kind == "array" then
			. as $array | $array.templates[]?.items[]
		| select(.autocomplete.enabled == true)
			| {block: $block.id, array: $array.id, field: .id}
		  else
			select(.autocomplete.enabled == true)
			| {block: $block.id, array: null, field: .id}
		  end
	' "$menu")

	# Validate and update every simple menu field with the value that the node
	# currently publishes. This reaches each node's type conversion and
	# validation path without inventing a configuration that starts external
	# resources.
	while IFS= read -r item; do
		message="$(jq -nc --arg menu "NODE${node}MENU" --argjson item "$item" '
			{
				type: "field.validate",
				menuId: $menu,
				field: {blockId: $item.block, fieldId: $item.field},
				value: $item.value
			}')"
		cli engine send-node-formular-msg --id "$node" --message "$message" >/dev/null

		message="$(jq -nc --arg menu "NODE${node}MENU" --argjson item "$item" '
			{
				type: "field.update",
				menuId: $menu,
				field: {blockId: $item.block, fieldId: $item.field},
				value: $item.value
			}')"
		cli engine send-node-formular-msg --id "$node" --message "$message" >/dev/null
	done < <(jq -c '
		.blocks[] as $block
		| $block.items[]
		| select(has("value"))
		| {block: $block.id, field: .id, value: .value}
	' "$menu")

	# Apply every form block with its published values. The assertion is that a
	# round trip of current state is accepted and keeps the node available.
	jq '
		reduce (.blocks[] | select(.form == true)) as $block ({};
			.[$block.id] = (reduce ($block.items[] | select(has("value"))) as $item ({};
				.[$item.id] = $item.value)))
	' "$menu" >"$desired"
	if [[ "$(jq 'length' "$desired")" != 0 ]]; then
		cli graph apply-menu --id "$node" <"$desired" >/dev/null \
			|| fail "$class did not accept its published form state"
	fi
	cli graph node --id "$node" >/dev/null \
		|| fail "$class disappeared after menu validation"
}

start_daemon

cli graph classes >"$E2E_RUNTIME/classes.json"
mapfile -t classes < <(jq -r '.classes[].class' "$E2E_RUNTIME/classes.json")
(( ${#classes[@]} > 20 )) || fail "too few node classes were registered"

node_ids=()
node_classes=()
index=0
for class in "${classes[@]}"; do
	cli engine node-class-snapshot --class "$class" | jq -e --arg class "$class" \
		'.snapshot.class == $class' >/dev/null \
		|| fail "$class returned an invalid class snapshot"
	cli engine node-class-long-description --class "$class" | jq -e \
		'.description | type == "string"' >/dev/null \
		|| fail "$class returned an invalid long description"

	name="Surface $index"
	if ! cli engine add-node --class "$class" --name "$name" \
		>"$E2E_RUNTIME/add-${index}.json" 2>"$E2E_RUNTIME/add-${index}.err"; then
		# A class can reject its default state on this platform. Class metadata is
		# still covered above; only live instances take part in menu checks.
		((index += 1))
		continue
	fi
	node="$(jq -er '.id' "$E2E_RUNTIME/add-${index}.json")"
	node_ids+=("$node")
	node_classes+=("$class")
	cli engine node-snapshot --id "$node" | jq -e --arg class "$class" \
		'.snapshot.class == $class' >/dev/null \
		|| fail "$class returned an invalid node snapshot"
	((index += 1))
done

(( ${#node_ids[@]} > 20 )) || fail "too few node classes could be instantiated"

# Menu checks operate on independent, unlinked nodes. Run a small batch at a
# time because each check makes many request/response round trips.
menu_pids=()
menu_batch_failed=0
wait_menu_batch() {
	local pid
	for pid in "${menu_pids[@]}"; do
		if ! wait "$pid"; then
			menu_batch_failed=1
		fi
	done
	menu_pids=()
}
for node_index in "${!node_ids[@]}"; do
	(
		trap - EXIT
		exercise_menu "${node_classes[$node_index]}" "${node_ids[$node_index]}"
	) &
	menu_pids+=("$!")
	if (( ${#menu_pids[@]} >= ALMAGEST_E2E_MENU_JOBS )); then
		wait_menu_batch
	fi
done
wait_menu_batch
(( menu_batch_failed == 0 )) || fail "one or more node menu checks failed"

# Attach and detach compatible default ports across the live class instances.
# One pass gives every right port a provider. The second pass gives every left
# port a consumer. Failed combinations are expected because nodes can impose
# stricter semantic constraints than the shared port type.
cli engine workspace-snapshot >"$E2E_RUNTIME/link-surface.json"
jq -r '
	.snapshot as $workspace
	| [
		$workspace.nodes | to_entries[] as $node
		| ($node.value.right_ports[]? | tostring) as $id
		| {id: $id, node: $node.key, types: $workspace.ports[$id].types}
	] as $right
	| [
		$workspace.nodes | to_entries[] as $node
		| ($node.value.left_ports[]? | tostring) as $id
		| {id: $id, node: $node.key, types: $workspace.ports[$id].types}
	] as $left
	| $right[] as $r
	| $left[] as $l
	| select($r.node != $l.node)
	| select(any($r.types[]; . as $type | $l.types | index($type)))
	| [$r.id, $l.id, $r.node, $l.node] | @tsv
' "$E2E_RUNTIME/link-surface.json" >"$E2E_RUNTIME/link-candidates.tsv"

declare -A linked_right=()
declare -A linked_left=()
link_surface_count=0
exercise_link_pair() {
	local right_port="$1"
	local left_port="$2"
	local output="$E2E_RUNTIME/link-attempt.json"
	local link
	if ! cli graph add-link --port-a "$right_port" --port-b "$left_port" \
		>"$output" 2>/dev/null; then
		return 1
	fi
	link="$(jq -er '.id' "$output")" || return 1
	sleep 0.02
	cli graph delete-link --id "$link" >/dev/null 2>&1 || true
	linked_right[$right_port]=1
	linked_left[$left_port]=1
	((link_surface_count += 1))
}

while IFS=$'\t' read -r right_port left_port right_node left_node; do
	[[ -z "${linked_right[$right_port]:-}" ]] || continue
	exercise_link_pair "$right_port" "$left_port" "$right_node" "$left_node" || true
done <"$E2E_RUNTIME/link-candidates.tsv"

sort -t $'\t' -k2,2n -k1,1n "$E2E_RUNTIME/link-candidates.tsv" \
	>"$E2E_RUNTIME/link-candidates-by-left.tsv"
while IFS=$'\t' read -r right_port left_port right_node left_node; do
	[[ -z "${linked_left[$left_port]:-}" ]] || continue
	exercise_link_pair "$right_port" "$left_port" "$right_node" "$left_node" || true
done <"$E2E_RUNTIME/link-candidates-by-left.tsv"

# Exercise different implementations of the same shared link type. For
# example, Network consumers must handle OutNetwork, MockNetwork, logging, TLS,
# SOCKS, and routing providers. Bound the matrix so this remains a focused E2E
# case even if more classes are added later.
link_matrix_attempts=0
while IFS=$'\t' read -r right_port left_port right_node left_node; do
	(( link_matrix_attempts >= ALMAGEST_E2E_LINK_MATRIX_ATTEMPTS )) && break
	exercise_link_pair "$right_port" "$left_port" "$right_node" "$left_node" || true
	((link_matrix_attempts += 1))
done <"$E2E_RUNTIME/link-candidates.tsv"

(( link_surface_count >= 10 )) \
	|| fail "too few compatible class ports accepted links: $link_surface_count"

# Cover the remaining graph mutation surfaces on one disposable live node.
disposable="$(add_node almagest/MockNetwork "Surface Disposable")"
cli engine set-node-position --id "$disposable" --position "10,20" >/dev/null
raw_engine move_nodes "{\"workspace\":\"main\",\"positions\":{\"$disposable\":\"30,40\"}}" >/dev/null
cli engine set-node-name --id "$disposable" --new-name "Surface Renamed" >/dev/null
cli engine remove-node-popup --id "$disposable" --popup 999 >/dev/null
cli engine remove-node-popups-by-type --id "$disposable" --type warning >/dev/null
cli engine remove-node-popups --id "$disposable" >/dev/null
cli engine send-inbox --id "$disposable" --payload '{"kind":"ignored-e2e-message"}' >/dev/null
raw_engine trigger_node \
	"{\"workspace\":\"main\",\"id\":$disposable}" >/dev/null 2>&1 || true

copy_data="$(cli engine copy --ids "$disposable" | jq -er '.data')"
[[ -n "$copy_data" ]] || fail "copy returned an empty clipboard"
cli engine paste --data "$copy_data" | jq -e '.ids | length == 1' >/dev/null \
	|| fail "paste did not create one copied node"
cli engine replace-node-with-placeholder --id "$disposable" >/dev/null
cli graph node --id "$disposable" | jq -e '.snapshot.placeholder' >/dev/null \
	|| fail "node was not replaced with a placeholder"
cli engine undo >/dev/null
cli engine redo >/dev/null

# Unknown-workspace calls must take the library guards and must not mutate the
# real workspace. Methods that report a value return not-found/bad-request;
# void mutations return an empty response after the guarded no-op.
before_nodes="$(cli engine workspace-snapshot | jq -er '.snapshot.nodes | length')"
for command in \
	'workspace_snapshot {"workspace":"missing"}' \
	'workers_snapshot {"workspace":"missing"}' \
	'node_class_snapshot {"workspace":"missing","class":"almagest/MockNetwork"}' \
	'node_snapshot {"workspace":"missing","id":1}' \
	'port_snapshot {"workspace":"missing","id":1}' \
	'link_snapshot {"workspace":"missing","id":1}' \
	'node_class_long_description {"workspace":"missing","class":"almagest/MockNetwork"}' \
	'add_node {"workspace":"missing","class":"almagest/MockNetwork"}' \
	'replace_node_with_placeholder {"workspace":"missing","id":1}' \
	'add_link {"workspace":"missing","port_a":1,"port_b":2}' \
	'trigger_node {"workspace":"missing","id":1}'; do
	method="${command%% *}"
	payload="${command#* }"
	expect_raw_failure "missing-workspace-${method}" "$method" "$payload"
done

for command in \
	'remove_node {"workspace":"missing","id":1}' \
	'remove_nodes {"workspace":"missing","ids":[1,2]}' \
	'remove_nodes_by_class {"workspace":"missing","class":"almagest/MockNetwork"}' \
	'remove_link {"workspace":"missing","id":1}' \
	'set_node_position {"workspace":"missing","id":1,"position":"0,0"}' \
	'move_nodes {"workspace":"missing","positions":{"1":"0,0"}}' \
	'set_node_name {"workspace":"missing","id":1,"name":"ignored"}' \
	'remove_node_popup {"workspace":"missing","node":1,"popup":1}' \
	'remove_node_popups {"workspace":"missing","node":1}' \
	'remove_node_popups_by_type {"workspace":"missing","node":1,"type":"err"}' \
	'send_inbox {"workspace":"missing","node":1,"payload":null}' \
	'undo {"workspace":"missing"}' \
	'redo {"workspace":"missing"}' \
	'send_node_formular_msg {"workspace":"missing","node":1,"message":{"type":"field.update","menuId":"NODE1MENU","field":{"blockId":"config","fieldId":"ignored"},"value":null}}'; do
	method="${command%% *}"
	payload="${command#* }"
	raw_engine "$method" "$payload" >/dev/null
done
raw_engine copy '{"workspace":"missing","ids":[1]}' | jq -e '.data == ""' >/dev/null
raw_engine paste '{"workspace":"missing","data":"invalid"}' | jq -e '.ids == []' >/dev/null
[[ "$(cli engine workspace-snapshot | jq -er '.snapshot.nodes | length')" == "$before_nodes" ]] \
	|| fail "unknown-workspace calls changed the main workspace"

# Each switch arm must reject a payload with the wrong JSON type. This also
# proves that malformed requests do not terminate a session or the daemon.
for method in \
	subscribe_notifications unsubscribe_notifications request_full_snapshot \
	subscribe_node_menu unsubscribe_node_menu workspace_snapshot workers_snapshot \
	node_class_snapshot node_snapshot port_snapshot link_snapshot \
	node_class_long_description add_node replace_node_with_placeholder add_link \
	remove_node remove_nodes remove_nodes_by_class remove_link set_node_position \
	move_nodes set_node_name remove_node_popup remove_node_popups \
	remove_node_popups_by_type copy paste send_inbox trigger_node undo redo \
	send_node_formular_msg; do
	expect_raw_failure "invalid-payload-${method}" "$method" '"wrong-type"'
done

cli admin ping --nonce engine-surface-validation | \
	jq -e '.nonce == "engine-surface-validation"' >/dev/null \
	|| fail "daemon did not remain responsive"

log "case passed"
