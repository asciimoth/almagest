#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

tmp_socket=""
tmp_storage=""

cleanup() {
	stop_daemon
}

discover_socket() {
	tmp_socket="$(find "$E2E_RUNTIME" -maxdepth 1 -type s -name 'alm-sock-*' -print -quit)"
	[[ -n "$tmp_socket" ]]
}

discover_storage() {
	tmp_storage="$(find "$E2E_RUNTIME" -maxdepth 1 -type d -name 'alm-store-*' -print -quit)"
	[[ -n "$tmp_storage" ]]
}

assert_cli_failure() {
	local description="$1"
	local expected="$2"
	shift 2
	local output="$E2E_RUNTIME/${description}.out"
	local error="$E2E_RUNTIME/${description}.err"

	if "$@" >"$output" 2>"$error"; then
		fail "$description unexpectedly succeeded"
	fi
	grep -F -- "$expected" "$error" >/dev/null \
		|| fail "$description did not report: $expected"
}

: >"$E2E_DAEMON_LOG"
log "starting daemon from standard input"
XDG_RUNTIME_DIR="$E2E_RUNTIME" almagestd \
	--stdin \
	--debug-nodes \
	--experimental \
	--tmp-file-storage \
	--tmp-unix-admin \
	--no-pool \
	-l debug \
	<"$E2E_CONFIG_DIR/stdin.hujson" \
	>"$E2E_DAEMON_LOG" 2>&1 &
daemon_pid="$!"
export daemon_pid
trap cleanup EXIT

retry "$ALMAGEST_E2E_TIMEOUT" "temporary admin socket" discover_socket \
	|| fail "temporary admin socket was not created"
E2E_ADMIN_SOCK="$tmp_socket"
export E2E_ADMIN_SOCK
retry "$ALMAGEST_E2E_TIMEOUT" "admin API ping" \
	cli admin ping --nonce stdin-startup >"$E2E_RUNTIME/ping.json" \
	|| fail "admin API did not answer through the temporary socket"
jq -e '.nonce == "stdin-startup"' "$E2E_RUNTIME/ping.json" >/dev/null \
	|| fail "admin API returned an unexpected ping nonce"

wait_graph_node "Standard Input Node" \
	|| fail "standard-input graph node is missing"

cli secrets list >"$E2E_RUNTIME/secrets.json"
jq -e '
	.secrets
	| length == 1
	and .[0].key == "stdin-protected"
	and .[0].value == "stdin-protected-value"
	and .[0].read_only
	and .[0].source == "protected"
	and .[0].nodes == ["Standard Input Node"]
' "$E2E_RUNTIME/secrets.json" >/dev/null \
	|| fail "embedded protected secret was not loaded correctly"

cli config snapshot >"$E2E_RUNTIME/config-snapshot.json"
jq -e '.read_only and (.text | contains("Standard Input Node"))' \
	"$E2E_RUNTIME/config-snapshot.json" >/dev/null \
	|| fail "standard-input config snapshot is not read-only or lost the graph"
if jq -er '.text' "$E2E_RUNTIME/config-snapshot.json" \
	| grep -E 'Protected|stdin-protected|stdin-protected-value' >/dev/null; then
	fail "config snapshot disclosed embedded protected data"
fi

printf '%s\n' '{"nodes": {}}' >"$E2E_RUNTIME/replacement.hujson"
assert_cli_failure config-read-only "config is read-only" \
	cli config apply "$E2E_RUNTIME/replacement.hujson"
wait_graph_node "Standard Input Node" \
	|| fail "rejected config update changed the running graph"

retry "$ALMAGEST_E2E_TIMEOUT" "temporary file storage" discover_storage \
	|| fail "temporary file storage was not created below XDG_RUNTIME_DIR"
[[ "$tmp_socket" == "$E2E_RUNTIME"/alm-sock-* ]] \
	|| fail "temporary admin socket was created outside XDG_RUNTIME_DIR"
[[ "$tmp_storage" == "$E2E_RUNTIME"/alm-store-* ]] \
	|| fail "temporary file storage was created outside XDG_RUNTIME_DIR"

stop_daemon
[[ ! -e "$tmp_socket" ]] || fail "temporary admin socket remained after shutdown"
[[ ! -e "$tmp_storage" ]] || fail "temporary file storage remained after shutdown"

log "case passed"
