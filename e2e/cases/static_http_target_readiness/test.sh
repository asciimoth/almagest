#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

target_pid=""
blocker_pid=""

cleanup() {
	stop_static_http_target "$target_pid"
	stop_static_http_target "$blocker_pid"
}
trap cleanup EXIT

target_body="delayed static HTTP target"
target_log="$E2E_RUNTIME/delayed-target.log"
original_path="$PATH"
export E2E_REAL_PYTHON3
E2E_REAL_PYTHON3="$(command -v python3)"
export E2E_TEST_PYTHON_DELAY=1
PATH="/e2e/cases/$E2E_CASE:$PATH"

target_pid="$(start_static_http_target 127.0.0.1 18150 "$target_body" "$target_log")"

PATH="$original_path"
unset E2E_TEST_PYTHON_DELAY E2E_REAL_PYTHON3

body="$(curl --fail --silent --show-error http://127.0.0.1:18150/)"
[[ "$body" == "$target_body" ]] || fail "unexpected static HTTP target response: $body"

blocker_pid="$(start_static_http_target \
	127.0.0.1 18151 "bind blocker" "$E2E_RUNTIME/bind-blocker.log")"
duplicate_error="$E2E_RUNTIME/bind-duplicate-startup.log"
if duplicate_pid="$(start_static_http_target \
	127.0.0.1 18151 "duplicate" "$E2E_RUNTIME/bind-duplicate.log" 2>"$duplicate_error")"; then
	stop_static_http_target "$duplicate_pid"
	fail "static HTTP target reported readiness after a bind failure"
fi
grep -F "static HTTP target exited before it was ready" "$duplicate_error" >/dev/null \
	|| fail "static HTTP target did not report its startup failure"
grep -F "Address already in use" "$duplicate_error" >/dev/null \
	|| fail "static HTTP target did not report its bind error"

log "case passed"
