#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

ALMAGEST_E2E_LOG_LEVEL=info
start_daemon
cli log subscribe >"$E2E_RUNTIME/log-snapshot.json"

expected_mode="$ALMAGEST_E2E_DAEMON_MODE"

jq -e --arg mode "$expected_mode" '
  [.entries[] | select(.source == "daemon" and .level == "info") | .message] as $messages
  | ($messages | index("Daemon configuration loaded execution_mode=" + $mode)) as $loaded
  | ($messages | index("Daemon admin API ready")) as $ready
  | $loaded != null and $ready != null and $loaded < $ready
    and ([$messages[] | select(. == "Daemon configuration loaded execution_mode=" + $mode)] | length == 1)
    and ([$messages[] | select(. == "Daemon admin API ready")] | length == 1)
' "$E2E_RUNTIME/log-snapshot.json" >/dev/null \
	|| fail "daemon readiness entries are missing from the admin log snapshot"

grep -F 'Using file storage at ' "$E2E_DAEMON_LOG" >/dev/null \
	|| fail "operator startup output is missing from the process log"
grep -F 'Almagest version:' "$E2E_DAEMON_LOG" >/dev/null \
	|| fail "operator version output is missing from the process log"
jq -e '
  [.entries[].message | select(contains("Using file storage at ") or contains("Almagest version:"))] | length == 0
' "$E2E_RUNTIME/log-snapshot.json" >/dev/null \
	|| fail "operator output crossed into the admin log snapshot"

log "case passed"
