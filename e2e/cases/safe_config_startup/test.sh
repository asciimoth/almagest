#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

# The safe path checker intentionally rejects paths below world-writable /tmp.
# Use an isolated path below /var/lib inside this case's disposable container.
config_root="/var/lib/almagest-e2e-safe-config/nested"
config_file="$config_root/alm.hujson"
protected_file="$config_root/protected_alm.hujson"

cleanup() {
	stop_daemon
}

start_safe_daemon() {
	if [[ -n "${daemon_pid:-}" ]] && kill -0 "$daemon_pid" 2>/dev/null; then
		fail "daemon is already running"
	fi
	: >"$E2E_DAEMON_LOG"
	log "starting daemon with safe config handling"
	XDG_RUNTIME_DIR="$E2E_RUNTIME" almagestd \
		-c "$config_file" \
		-l debug \
		--debug-nodes \
		--experimental \
		--no-isolation \
		--unix "$E2E_ADMIN_SOCK" \
		--web 0 \
		>"$E2E_DAEMON_LOG" 2>&1 &
	daemon_pid="$!"
	trap cleanup EXIT
	retry "$ALMAGEST_E2E_TIMEOUT" "safe-startup admin socket" \
		test -S "$E2E_ADMIN_SOCK" \
		|| fail "safe-startup admin socket was not created"
	retry "$ALMAGEST_E2E_TIMEOUT" "safe-startup admin ping" \
		cli admin ping --nonce safe-config >/dev/null \
		|| fail "safe-startup admin API did not answer"
}

[[ ! -e "$config_root" ]] || fail "safe config root already exists"
start_safe_daemon

[[ -f "$config_file" ]] || fail "default config file was not created"
[[ -f "$protected_file" ]] || fail "default protected config file was not created"
[[ "$(stat -c '%a' "$config_root")" == "700" ]] \
	|| fail "safe config directory does not have mode 0700"
[[ "$(stat -c '%a' "$config_file")" == "600" ]] \
	|| fail "safe config file does not have mode 0600"
[[ "$(stat -c '%a' "$protected_file")" == "600" ]] \
	|| fail "safe protected config file does not have mode 0600"
[[ -d /var/lib/almagest/store ]] \
	|| fail "Linux default persistent storage was not created"
[[ "$(stat -c '%a' /var/lib/almagest/store)" == "700" ]] \
	|| fail "Linux default persistent storage does not have mode 0700"

printf '%s\n' '{"nodes":{"Safe Startup Node":{"Class":"almagest/StringConstant","value":"persisted"}}}' \
	>"$E2E_RUNTIME/replacement.hujson"
cli config apply "$E2E_RUNTIME/replacement.hujson" >/dev/null
wait_graph_node "Safe Startup Node" \
	|| fail "applied safe config did not create its node"
grep -F 'Safe Startup Node' "$config_file" >/dev/null \
	|| fail "config update was not written to the safe config file"

stop_daemon
start_safe_daemon
wait_graph_node "Safe Startup Node" \
	|| fail "safe config did not persist across restart"
cli graph map | jq -e '.nodes["Safe Startup Node"]' >/dev/null \
	|| fail "persisted safe config node is absent"

stop_daemon
log "case passed"
