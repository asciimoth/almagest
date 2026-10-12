#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

output="$E2E_ISOLATION_ROOT/helper/command-node.log"

line_count() {
	if [[ ! -f "$output" ]]; then
		echo 0
		return
	fi
	wc -l <"$output"
}

output_line_is() {
	local line="$1"
	local expected="$2"
	[[ "$(sed -n "${line}p" "$output" 2>/dev/null)" == "$expected" ]]
}

wait_output_line() {
	local line="$1"
	local expected="$2"
	retry "$ALMAGEST_E2E_TIMEOUT" "command output line $line" \
		output_line_is "$line" "$expected" \
		|| fail "command output line $line did not become: $expected"
}

trigger_node() {
	local id="$1"
	cli raw --surface engine --method trigger_node \
		--payload "$(jq -nc --argjson id "$id" '{workspace:"main",id:$id}')" >/dev/null
}

left_port_names() {
	local node="$1"
	cli engine workspace-snapshot | jq -cr --argjson node "$node" '
		.snapshot as $workspace
		| [$workspace.nodes[($node | tostring)].left_ports[]
			| . as $port
			| $workspace.ports[($port | tostring)].name]
	'
}

left_port_names_are() {
	local node="$1"
	local expected="$2"
	[[ "$(left_port_names "$node")" == "$expected" ]]
}

wait_left_port_names() {
	local node="$1"
	local expected="$2"
	retry "$ALMAGEST_E2E_TIMEOUT" "command input ports $expected" \
		left_port_names_are "$node" "$expected" \
		|| fail "command input ports are $(left_port_names "$node"), want $expected"
}

command_menu_is_valid() {
	local node="$1"
	cli graph menu --id "$node" | jq -e '
		.blocks[]
		| select(.id == "config")
		| .items[]
		| select(.id == "command")
		| .value == "record-alpha"
		and .allowedValues == ["record-alpha", "record-beta"]
		and ((.status // "") != "error")
	' >/dev/null
}

command_menu_has_error() {
	local node="$1"
	cli graph menu --id "$node" | jq -e '
		.blocks[]
		| select(.id == "config")
		| .items[]
		| select(.id == "command")
		| .value == "record-alpha"
		and .status == "error"
		and (.statusText | contains("command is not allowed for nodes"))
	' >/dev/null
}

command_menu_status_is() {
	local node="$1"
	local status="$2"
	cli graph menu --id "$node" | jq -e --arg status "$status" '
		.blocks[]
		| select(.id == "config")
		| .items[]
		| select(.id == "command")
		| .value == "record-alpha" and .status == $status
	' >/dev/null
}

validate_command() {
	local node="$1"
	local value="$2"
	local message
	message="$(jq -nc \
		--arg menu "NODE${node}MENU" \
		--arg value "$value" \
		'{
			type:"field.validate",
			menuId:$menu,
			field:{blockId:"config",fieldId:"command"},
			value:$value
		}')"
	cli engine send-node-formular-msg --id "$node" --message "$message" >/dev/null
}

apply_command() {
	local value="$1"
	jq -nc --arg value "$value" '{config:{command:$value}}' \
		| cli graph apply-menu --name Command >/dev/null
}

start_daemon

for name in "Command Trigger" Message Optional Other Command; do
	wait_graph_node "$name" || fail "$name node is missing"
done

command_node="$(node_id_by_name Command)"
trigger="$(node_id_by_name "Command Trigger")"
message="$(node_id_by_name Message)"
optional="$(node_id_by_name Optional)"
other="$(node_id_by_name Other)"

wait_node_label "$command_node" record-alpha \
	|| fail "command node did not restore record-alpha"
wait_left_port_names "$command_node" '["Trigger","slot message value","slot optional value"]'
retry "$ALMAGEST_E2E_TIMEOUT" "filtered command menu" \
	command_menu_is_valid "$command_node" \
	|| fail "command menu did not contain only node-enabled commands"

# The trigger source exercises graph event delivery into the command node.
trigger_node "$trigger"
wait_output_line 1 'alpha|hello|extra'

# A direct command-node trigger exercises its own trigger callback.
trigger_node "$command_node"
wait_output_line 2 'alpha|hello|extra'

# Removing a required input must prevent command execution. Relinking must
# request the constant's current value before the next trigger.
message_link="$(link_id_by_port_names Message output Command "slot message value")"
cli graph delete-link --id "$message_link" >/dev/null
before_missing="$(line_count)"
trigger_node "$command_node"
sleep 0.2
[[ "$(line_count)" == "$before_missing" ]] \
	|| fail "command ran without its required message slot"
add_link_by_port_name "$message" right output "$command_node" left "slot message value" >/dev/null
trigger_node "$command_node"
wait_output_line "$((before_missing + 1))" 'alpha|hello|extra'

# Field validation reports an invalid protected selection without changing the
# current value, then clears its status for a valid selection.
validate_command "$command_node" hidden-command
retry "$ALMAGEST_E2E_TIMEOUT" "hidden command field validation" \
	command_menu_status_is "$command_node" error \
	|| fail "hidden command field validation did not report an error"
validate_command "$command_node" record-alpha
retry "$ALMAGEST_E2E_TIMEOUT" "valid command field validation" \
	command_menu_status_is "$command_node" ok \
	|| fail "valid command field validation did not clear the error"

# A protected command without AllowNodes cannot replace the current command.
apply_command hidden-command
retry "$ALMAGEST_E2E_TIMEOUT" "rejected hidden command form" \
	command_menu_has_error "$command_node" \
	|| fail "hidden command selection did not report a form error"
wait_node_label "$command_node" record-alpha \
	|| fail "hidden command selection replaced the active command"

# Switching commands removes obsolete ports and their links. The new input is
# then linked and used by the real command runner.
apply_command record-beta
wait_node_label "$command_node" record-beta \
	|| fail "command node did not select record-beta"
wait_left_port_names "$command_node" '["Trigger","slot other value"]'
add_link_by_port_name "$other" right output "$command_node" left "slot other value" >/dev/null
before_beta="$(line_count)"
trigger_node "$trigger"
wait_output_line "$((before_beta + 1))" 'beta|second'

# Clearing the command removes all slot ports and makes triggers inert. Going
# back to alpha must create clean ports with no stale string values or links.
apply_command ''
wait_node_label "$command_node" 'select command' \
	|| fail "command node did not enter the unconfigured state"
wait_left_port_names "$command_node" '["Trigger"]'
before_disabled="$(line_count)"
trigger_node "$command_node"
sleep 0.2
[[ "$(line_count)" == "$before_disabled" ]] \
	|| fail "unconfigured command node dispatched a command"

apply_command record-alpha
wait_node_label "$command_node" record-alpha \
	|| fail "command node did not restore record-alpha"
wait_left_port_names "$command_node" '["Trigger","slot message value","slot optional value"]'
trigger_node "$command_node"
sleep 0.2
[[ "$(line_count)" == "$before_disabled" ]] \
	|| fail "restored command retained stale slot values"
add_link_by_port_name "$message" right output "$command_node" left "slot message value" >/dev/null
add_link_by_port_name "$optional" right output "$command_node" left "slot optional value" >/dev/null
trigger_node "$command_node"
wait_output_line "$((before_disabled + 1))" 'alpha|hello|extra'

# Shutdown flushes autosave. Restart must preserve both the selected command and
# the rebuilt links, and must request current constant values during startup.
before_restart="$(line_count)"
stop_daemon
start_daemon
command_node="$(node_id_by_name Command)"
trigger="$(node_id_by_name "Command Trigger")"
wait_node_label "$command_node" record-alpha \
	|| fail "restart did not preserve record-alpha"
wait_left_port_names "$command_node" '["Trigger","slot message value","slot optional value"]'
trigger_node "$trigger"
wait_output_line "$((before_restart + 1))" 'alpha|hello|extra'

log "case passed"
