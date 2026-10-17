#!/usr/bin/env bash
set -euo pipefail

# shellcheck disable=SC1091 # This script runs from the E2E image.
source /e2e/lib/common.sh

menu_text() {
	local node="$1"
	local block="$2"
	local field="$3"
	cli graph menu --name "$node" | jq -er \
		--arg block "$block" --arg field "$field" \
		'.blocks[] | select(.id == $block) | .items[] | select(.id == $field) | (.value // .text // "")'
}

body="TorDriver discovered program response"
target_log="$E2E_RUNTIME/target.log"
case "$ALMAGEST_E2E_ISOLATION_MODE/$ALMAGEST_E2E_SANDBOX_MODE" in
	off/off) target_port=18091 ;;
	off/auto) target_port=18092 ;;
	off/strict) target_port=18093 ;;
	auto/off) target_port=18094 ;;
	auto/auto) target_port=18095 ;;
	auto/strict) target_port=18096 ;;
	strict/off) target_port=18097 ;;
	strict/auto) target_port=18098 ;;
	strict/strict) target_port=18099 ;;
esac
target_pid="$(start_static_http_target 127.0.0.1 "$target_port" "$body" "$target_log")"
trap 'kill "$target_pid" 2>/dev/null || true; cleanup_daemon' EXIT

request_through_tor() {
	curl --fail --silent --show-error --connect-timeout 3 --max-time 10 \
		--socks5-hostname 127.0.0.1:1081 "http://127.0.0.1:$target_port/discovered" \
		--output "$E2E_RUNTIME/response.txt" \
		&& [[ "$(cat "$E2E_RUNTIME/response.txt")" == "$body" ]]
}

request_through_tor_fails() {
	! request_through_tor
}

find_helper_pid() {
	local arguments command_line pid
	for arguments in /proc/[0-9]*/cmdline; do
		command_line="$(tr '\0' ' ' <"$arguments" 2>/dev/null || true)"
		[[ "$command_line" == *" -f "*torrc* ]] || continue
		pid="${arguments#/proc/}"
		printf '%s\n' "${pid%/cmdline}"
		return 0
	done
	return 1
}

helper_pid_changed() {
	local previous="$1"
	local current
	current="$(find_helper_pid)" || return 1
	[[ "$current" != "$previous" ]] && ! kill -0 "$previous" 2>/dev/null
}

helper_pid_stopped() {
	! kill -0 "$1" 2>/dev/null
}

assert_runtime_lifecycle() {
	local tor_node="$1"
	local out_node="$2"
	local helper_pid input_link output_link proxy_node replacement_out
	helper_pid="$(find_helper_pid)" || fail "Tor helper process is missing"
	proxy_node="$(node_id_by_name "Tor proxy")"

	# A consumer is not a Tor runtime dependency. Removing and restoring it
	# must not restart Tor or change its ready state.
	output_link="$(link_id_by_port_names "Tor proxy" proxy Tor "Network default")"
	cli graph delete-link --id "$output_link" >/dev/null
	[[ "$(node_label "$tor_node")" == up ]] \
		|| fail "TorDriver left the ready state after its output consumer was removed"
	retry "$ALMAGEST_E2E_TIMEOUT" "closed Tor consumer" request_through_tor_fails \
		|| fail "removed Tor output consumer kept accepting traffic"
	add_link_by_port_name "$proxy_node" right proxy "$tor_node" left "Network default" >/dev/null
	retry "$ALMAGEST_E2E_TIMEOUT" "traffic after Tor consumer reconnect" request_through_tor \
		|| fail "Tor output consumer did not recover"
	[[ "$(find_helper_pid)" == "$helper_pid" ]] \
		|| fail "output consumer reconnect restarted Tor"

	# Replace the provider with another OutNetwork. The Tor process must stay
	# owned while the node withdraws and then republishes its output.
	replacement_out="$(add_node almagest/OutNetwork "Alternate Tor outbound")"
	input_link="$(link_id_by_port_names Tor out "Tor outbound" Network)"
	cli graph delete-link --id "$input_link" >/dev/null
	wait_node_label "$tor_node" "waiting for Network" \
		|| fail "TorDriver did not withdraw after input removal"
	retry "$ALMAGEST_E2E_TIMEOUT" "traffic blocked without Tor input" request_through_tor_fails \
		|| fail "Tor traffic continued without an input Network"
	add_link_by_port_name "$tor_node" right out "$replacement_out" left Network >/dev/null
	wait_node_label "$tor_node" up || fail "TorDriver did not accept a replacement input"
	retry "$ALMAGEST_E2E_TIMEOUT" "traffic through replacement input" request_through_tor \
		|| fail "replacement Tor input did not carry traffic"
	[[ "$(find_helper_pid)" == "$helper_pid" ]] \
		|| fail "input replacement restarted Tor"

	# Repeated detach/attach operations exercise coalesced wakeups and stale
	# readiness results. The final desired link must win each time.
	for _ in 1 2 3; do
		input_link="$(link_id_by_port_names Tor out "Alternate Tor outbound" Network)"
		cli graph delete-link --id "$input_link" >/dev/null
		add_link_by_port_name "$tor_node" right out "$replacement_out" left Network >/dev/null
	done
	wait_node_label "$tor_node" up || fail "TorDriver did not converge after rapid input churn"
	retry "$ALMAGEST_E2E_TIMEOUT" "traffic after rapid Tor input churn" request_through_tor \
		|| fail "Tor traffic did not recover after rapid input churn"
	[[ "$(find_helper_pid)" == "$helper_pid" ]] \
		|| fail "rapid input churn restarted Tor"

	# The explicit control action must replace the owned process and converge
	# with both graph links still present.
	cli graph click-menu-button --node "$tor_node" --block control --button restart >/dev/null
	retry "$ALMAGEST_E2E_TIMEOUT" "Tor process replacement after restart" helper_pid_changed "$helper_pid" \
		|| fail "Tor restart did not replace its helper process"
	helper_pid="$(find_helper_pid)"
	wait_node_label "$tor_node" up || fail "TorDriver did not recover after restart"
	retry "$ALMAGEST_E2E_TIMEOUT" "traffic after Tor restart" request_through_tor \
		|| fail "Tor traffic did not recover after restart"

	# Deleting the runtime owner must stop the child. A new default TorDriver
	# must work when it is created and linked through the admin API.
	cli graph delete-node --id "$tor_node" >/dev/null
	retry "$ALMAGEST_E2E_TIMEOUT" "Tor process exit after node deletion" \
		helper_pid_stopped "$helper_pid" \
		|| fail "deleted TorDriver kept its child process"
	tor_node="$(add_node almagest/TorDriver Tor)"
	add_link_by_port_name "$tor_node" right out "$replacement_out" left Network >/dev/null
	add_link_by_port_name "$proxy_node" right proxy "$tor_node" left "Network default" >/dev/null
	wait_node_label "$tor_node" up || fail "dynamically created TorDriver did not become ready"
	retry "$ALMAGEST_E2E_TIMEOUT" "traffic through recreated TorDriver" request_through_tor \
		|| fail "dynamically created TorDriver did not carry traffic"

	# Keep the original provider in the graph so the missing-program restart
	# also proves that unrelated and unused nodes do not affect discovery.
	cli graph delete-node --id "$replacement_out" >/dev/null
	add_link_by_port_name "$tor_node" right out "$out_node" left Network >/dev/null
	wait_node_label "$tor_node" up || fail "TorDriver did not return to its original input"
}

assert_discovered_runtime() {
	local tor_node
	tor_node="$(node_id_by_name Tor)"
	wait_node_label "$tor_node" "waiting for Network" \
		|| fail "TorDriver did not wait for its missing input network"
	# Keep an output consumer connected while the input is absent. The Tor
	# process can attempt bootstrap during this interval, but that must not turn
	# the intentionally missing input into a latched runtime failure.
	sleep 2
	[[ "$(node_label "$tor_node")" == "waiting for Network" ]] \
		|| fail "TorDriver treated its missing input network as a runtime failure"
	local out_node
	out_node="$(node_id_by_name "Tor outbound")"
	add_link_by_port_name "$tor_node" right out "$out_node" left Network >/dev/null
	wait_node_label "$tor_node" up || fail "discovered TorDriver did not become ready"
	retry "$ALMAGEST_E2E_TIMEOUT" "discovered Tor startup log" \
		grep -F -e "Discovered sandbox program tor at /usr/local/bin/tor-e2e-helper" \
		-e "DEBUG sandbox discovered program=tor path=/usr/local/bin/tor-e2e-helper" "$E2E_DAEMON_LOG" \
		|| fail "daemon did not report the discovered Tor executable"
	retry "$ALMAGEST_E2E_TIMEOUT" "optional obfs discovery warning" \
		grep -F -e "Sandbox program obfs4 is unavailable" \
		-e "WARN sandbox program=obfs4 unavailable" "$E2E_DAEMON_LOG" \
		|| fail "daemon did not report the missing optional obfs program"
	retry "$ALMAGEST_E2E_TIMEOUT" "request through discovered Tor program" request_through_tor \
		|| fail "discovered Tor program did not carry traffic"
	cli admin hello --client-name tor-driver-discovery >"$E2E_RUNTIME/hello.json"
	if [[ "$ALMAGEST_E2E_SANDBOX_MODE" == off ]]; then
		jq -e '.subprogram_sandbox.templates.tor.profile == "direct-v1" and (.subprogram_sandbox.templates.tor.strict_satisfied | not)' \
			"$E2E_RUNTIME/hello.json" >/dev/null \
			|| { cat "$E2E_RUNTIME/hello.json" >&2; fail "unexpected off sandbox status"; }
	else
		jq -e '.subprogram_sandbox.templates.tor.profile == "linux-full-v1" and .subprogram_sandbox.templates.tor.strict_satisfied and .subprogram_sandbox.templates.tor.full_boundary' \
			"$E2E_RUNTIME/hello.json" >/dev/null \
			|| { cat "$E2E_RUNTIME/hello.json" >&2; fail "unexpected protected sandbox status"; }
	fi
	assert_runtime_lifecycle "$tor_node" "$out_node"
}

tor_has_missing_program_error() {
	[[ "$(menu_text Tor status state)" == "error" \
		&& "$(menu_text Tor status error)" == *'executable "tor" was not found in the host search path'* ]]
}

# Direct runs exercise the protected-config opt-in. Isolated runs exercise the
# CLI/environment opt-in with the protected switch disabled. The full matrix
# therefore covers both entry points without restarting an isolated daemon in
# the same delegated cgroup.
if [[ "$ALMAGEST_E2E_ISOLATION_MODE" != off ]]; then
	sed -i 's/"DiscoverSystemPrograms": true/"DiscoverSystemPrograms": false/' protected_alm.hujson
	export ALMAGEST_DISCOVER_SYSTEM_PROGRAMS=1
fi
start_daemon
assert_discovered_runtime
stop_daemon

# Direct mode can restart in the same case without reusing an isolation
# cgroup. Verify that a missing required program is a node-local UI error and
# does not stop the daemon or an unrelated node.
if [[ "$ALMAGEST_E2E_ISOLATION_MODE" == off ]]; then
	rm -f /usr/local/bin/tor
	start_daemon
	retry "$ALMAGEST_E2E_TIMEOUT" "Tor missing-program interface error" \
		tor_has_missing_program_error \
		|| fail "TorDriver did not expose the missing discovered program in its interface"
	cli graph menu --name "Unaffected mock" | jq -e '.blocks[] | select(.id == "config")' >/dev/null \
		|| fail "unrelated MockNetwork node stopped after Tor discovery failed"
	stop_daemon
fi

log "case passed"
