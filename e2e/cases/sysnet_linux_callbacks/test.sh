#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

events="$E2E_RUNTIME/callbacks/events.log"
killswitch_pid=""
killswitch_sock="$E2E_RUNTIME/killswitch.sock"

cleanup() {
	stop_daemon
	stop_killswitch
	if [[ -x "$E2E_RUNTIME/cleanup-callbacks.sh" ]]; then
		"$E2E_RUNTIME/cleanup-callbacks.sh"
	fi
}

delete_node() {
	local id="$1"
	cli graph delete-node --id "$id" >/tmp/e2e-sysnet-callbacks-delete-node.json
}

start_killswitch() {
	if [[ -n "$killswitch_pid" ]] && kill -0 "$killswitch_pid" 2>/dev/null; then
		fail "killswitch is already running"
	fi
	: >"$E2E_RUNTIME/killswitch.log"
	log "starting killswitch"
	killswitch "$E2E_RUNTIME/killswitch.json" >"$E2E_RUNTIME/killswitch.log" 2>&1 &
	killswitch_pid="$!"
	retry "$ALMAGEST_E2E_TIMEOUT" "killswitch admin API socket" test -S "$killswitch_sock" \
		|| fail_killswitch "killswitch admin API socket was not created"
	retry "$ALMAGEST_E2E_TIMEOUT" "killswitch managed almcb0 interface" \
		killswitch_has_attached_interface \
		|| fail_killswitch "killswitch did not attach to almcb0"
}

stop_killswitch() {
	if [[ -z "${killswitch_pid:-}" ]]; then
		return 0
	fi
	if kill -0 "$killswitch_pid" 2>/dev/null; then
		log "stopping killswitch"
		kill "$killswitch_pid" 2>/dev/null || true
		local deadline=$((SECONDS + 10))
		while kill -0 "$killswitch_pid" 2>/dev/null && ((SECONDS <= deadline)); do
			sleep 0.25
		done
		if kill -0 "$killswitch_pid" 2>/dev/null; then
			kill -KILL "$killswitch_pid" 2>/dev/null || true
		fi
	fi
	wait "$killswitch_pid" 2>/dev/null || true
	killswitch_pid=""
}

fail_killswitch() {
	log "FAILED: $*"
	if [[ -f "$E2E_RUNTIME/killswitch.log" ]]; then
		log "killswitch log follows"
		tail -200 "$E2E_RUNTIME/killswitch.log" >&2 || true
	fi
	fail "$*"
}

killswitch_cfg() {
	killswitch-cli get-cfg -s "$killswitch_sock" --json-out
}

killswitch_has_attached_interface() {
	killswitch_cfg | jq -e '.effective_interfaces[]? | select(.name == "almcb0" and .attached == true)' >/dev/null
}

event_contains() {
	local text="$1"
	grep -F -- "$text" "$events" >/dev/null
}

wait_event_contains() {
	local text="$1"
	retry "$ALMAGEST_E2E_TIMEOUT" "callback event containing $text" \
		event_contains "$text"
}

dump_events() {
	if [[ -f "$events" ]]; then
		log "callback events follow"
		cat "$events" >&2 || true
	else
		log "callback events file does not exist: $events"
	fi
}

line_no() {
	local prefix="$1"
	grep -n -F -- "$prefix" "$events" | head -n 1 | cut -d: -f1
}

assert_event_before() {
	local first="$1"
	local second="$2"
	local first_line
	local second_line
	first_line="$(line_no "$first")"
	second_line="$(line_no "$second")"
	[[ -n "$first_line" && -n "$second_line" ]] \
		|| fail "missing callback event order entries: $first / $second"
	((first_line < second_line)) \
		|| fail "callback event $first did not precede $second"
}

api_log_contains_text() {
	local text="$1"
	cli log subscribe | jq -e --arg text "$text" \
		'.entries[] | select(.source == "daemon" and (.message | contains($text)))' >/dev/null
}

wait_api_log_contains_text() {
	local text="$1"
	retry "$ALMAGEST_E2E_TIMEOUT" "API log containing $text" \
		api_log_contains_text "$text"
}

assert_api_log_not_contains_text() {
	local text="$1"
	if api_log_contains_text "$text"; then
		fail "API log unexpectedly contains: $text"
	fi
}

assert_daemon_log_not_contains() {
	local text="$1"
	if grep -F -- "$text" "$E2E_DAEMON_LOG" >/dev/null; then
		fail "daemon log unexpectedly contains: $text"
	fi
}

trap cleanup EXIT

start_killswitch
start_daemon

wait_node_class_available "almagest/Tun" \
	|| fail "Tun node class is unavailable"
wait_node_class_available "almagest/DefaultTun" \
	|| fail "DefaultTun node class is unavailable"
wait_graph_node "Tun callbacks 1601" || fail "Tun callback node missing"
wait_graph_node "DefaultTun callbacks 1602" || fail "DefaultTun callback node missing"

tun_node="$(node_id_by_name "Tun callbacks 1601")"
default_tun="$(node_id_by_name "DefaultTun callbacks 1602")"

wait_node_label "$tun_node" cbtun0 || fail "Tun did not become ready as cbtun0"
wait_node_label "$default_tun" ready || fail "DefaultTun did not become ready"

wait_event_contains "tun-created|uid=24020|gid=24020|env=tun-created|argc=3|sysnet-linux.tun-created|" \
	|| { dump_events; fail "TunCreated command did not record expected UID/GID/env/template args"; }
wait_event_contains "|argv-secret-token" \
	|| fail "TunCreated command did not preserve non-empty secret argv after dropping empty template arg"
wait_event_contains "tun-configured|uid=0|gid=0|env=tun-configured|argc=3|sysnet-linux.tun-configured|" \
	|| fail "TunConfigured command did not record expected template args"
wait_event_contains "killswitch-updated|uid=0|gid=0|env=killswitch-updated|argc=2|sysnet-linux.killswitch-updated|0xeb9f0001" \
	|| fail "KillswitchUpdated command did not record expected rules"
wait_event_contains "default-created|uid=0|gid=0|env=default-created|argc=3|sysnet-linux.default-tun-created|" \
	|| fail "DefaultTunCreated command did not record expected callback"
wait_event_contains "routing-applied|uid=0|gid=0|env=routing-applied|argc=2|sysnet-linux.routing-applied|" \
	|| fail "RoutingApplied command did not record expected config"
wait_event_contains "dns-configured|uid=0|gid=0|env=dns-configured|argc=2|sysnet-linux.dns-configured|" \
	|| fail "DNSConfigured command did not record expected server"
wait_event_contains "default-configured|uid=0|gid=0|env=default-configured|argc=2|sysnet-linux.default-tun-configured|" \
	|| fail "DefaultTunConfigured command did not record expected callback"

assert_event_before "killswitch-updated|" "default-created|"
assert_event_before "default-created|" "routing-applied|"
assert_event_before "routing-applied|" "dns-configured|"
assert_event_before "dns-configured|" "default-configured|"

wait_api_log_contains_text 'command "sysnet-linux.tun-created" finished: status=success' \
	|| fail "API log did not expose TunCreated command success"
wait_api_log_contains_text 'command "sysnet-linux.killswitch-updated" finished: status=timeout' \
	|| fail "API log did not expose KillswitchUpdated command timeout"
wait_api_log_contains_text 'command "sysnet-linux.default-tun-configured" finished: status=success' \
	|| fail "API log did not expose DefaultTunConfigured command success"

assert_api_log_not_contains_text "argv-secret-token"
assert_api_log_not_contains_text "default-argv-secret-token"
assert_api_log_not_contains_text "stdout-secret-token"
assert_api_log_not_contains_text "default-stdout-secret-token"
assert_daemon_log_not_contains "stdout-secret-token"
assert_daemon_log_not_contains "default-stdout-secret-token"

delete_node "$default_tun"
wait_event_contains "default-closed|uid=0|gid=0|env=default-closed|argc=1|sysnet-linux.default-tun-closed" \
	|| fail "DefaultTunClosed command did not record expected callback"
wait_api_log_contains_text 'command "sysnet-linux.default-tun-closed" finished: status=success' \
	|| fail "API log did not expose DefaultTunClosed command success"

log "case passed"
