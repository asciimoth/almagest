#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

export ALMAGEST_E2E_TMP_FILE_STORAGE=0
export ALMAGEST_E2E_FILE_STORAGE=/var/lib/almagest-e2e-snapshots-validation

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

start_daemon

for operation in export delete restore; do
	assert_cli_failure "snapshot-malformed-${operation}" \
		"invalid_payload: snapshot is invalid: malformed UUID" \
		cli snapshots "$operation" malformed
done
assert_cli_failure snapshot-malformed-rename \
	"invalid_payload: snapshot is invalid: malformed UUID" \
	cli snapshots rename --name rejected malformed

missing_uuid="00000000-0000-4000-8000-000000000001"
for operation in export delete restore; do
	assert_cli_failure "snapshot-missing-${operation}" \
		"not_found: snapshot was not found" \
		cli snapshots "$operation" "$missing_uuid"
done
assert_cli_failure snapshot-missing-rename \
	"not_found: snapshot was not found" \
	cli snapshots rename --name rejected "$missing_uuid"

: >"$E2E_RUNTIME/empty.zip"
assert_cli_failure snapshot-empty-import \
	"invalid_payload: snapshot archive is required" \
	cli snapshots import "$E2E_RUNTIME/empty.zip"
printf '%s\n' 'this is not a ZIP archive' >"$E2E_RUNTIME/not-a-zip.zip"
assert_cli_failure snapshot-invalid-import \
	"invalid_payload: snapshot is invalid" \
	cli snapshots import "$E2E_RUNTIME/not-a-zip.zip"

long_name="$(printf 'n%.0s' {1..257})"
assert_cli_failure snapshot-long-name-create \
	"too_large: snapshot exceeds the size limit" \
	cli snapshots create --name "$long_name"

cli snapshots create --name valid >"$E2E_RUNTIME/created.json"
snapshot_uuid="$(jq -er '.uuid' "$E2E_RUNTIME/created.json")"
cli snapshots export --output "$E2E_RUNTIME/valid.zip" "$snapshot_uuid" \
	>"$E2E_RUNTIME/exported.json"

assert_cli_failure snapshot-long-name-rename \
	"too_large: snapshot exceeds the size limit" \
	cli snapshots rename --name "$long_name" "$snapshot_uuid"
assert_cli_failure snapshot-duplicate-import \
	"invalid_payload: snapshot is invalid" \
	cli snapshots import "$E2E_RUNTIME/valid.zip"

jq -e --arg uuid "$snapshot_uuid" '
	.snapshots
	| length == 1
	and .[0].uuid == $uuid
	and .[0].name == "valid"
' <(cli snapshots list) >/dev/null \
	|| fail "rejected snapshot operations changed the valid snapshot"
cli snapshots export --output "$E2E_RUNTIME/valid-after-errors.zip" "$snapshot_uuid" >/dev/null
cmp "$E2E_RUNTIME/valid.zip" "$E2E_RUNTIME/valid-after-errors.zip" \
	|| fail "rejected snapshot operations changed the stored archive"
cli admin ping --nonce snapshot-validation | \
	jq -e '.nonce == "snapshot-validation"' >/dev/null \
	|| fail "daemon did not remain responsive after rejected snapshot operations"

log "case passed"
