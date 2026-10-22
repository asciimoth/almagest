#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

start_daemon

jq -e '
	.snapshots | length == 1
	and .[0].example == "001-hello-network"
	and .[0].name == "Hello Network"
	and (.[0].comment | length > 0)
	and (.[0] | has("uuid") | not)
	and (.[0] | has("timestamp") | not)
' <(cli snapshots list) >/dev/null || fail "example listing metadata is invalid"

wait_graph_node "Before Example" || fail "initial graph is missing"
cli snapshots load 001-hello-network >"$E2E_RUNTIME/loaded.json"
jq -e '
	.snapshot.example == "001-hello-network"
	and .snapshot.name == "Hello Network"
	and (.snapshot | has("uuid") | not)
	and (.snapshot | has("timestamp") | not)
' "$E2E_RUNTIME/loaded.json" >/dev/null || fail "example load response is invalid"

wait_graph_node "Outside Network Gateway" || fail "example gateway was not loaded"
wait_graph_node "Http Client" || fail "example HTTP client was not loaded"
if node_id_by_name "Before Example" >/dev/null 2>&1; then
	fail "old graph survived example loading"
fi
grep -F 'Outside Network Gateway' "$E2E_CONFIG_DIR/alm.hujson" >/dev/null \
	|| fail "loaded example config was not persisted"

log "case passed"
