#!/usr/bin/env bash
set -euo pipefail

# shellcheck disable=SC1091 # The harness provides this file in the E2E image.
source /e2e/lib/common.sh

export ALMAGEST_E2E_TMP_FILE_STORAGE=0
export ALMAGEST_E2E_FILE_STORAGE="$E2E_RUNTIME/storage"

cleanup() {
	stop_daemon
	if mountpoint -q "$E2E_ISOLATION_ROOT/runtime"; then
		umount "$E2E_ISOLATION_ROOT/runtime"
	fi
}

start_daemon
trap cleanup EXIT

cli flags get | jq -e '. == {}' >/dev/null || fail "missing flags file did not load as empty"
empty_snapshot_uuid="$(cli snapshots create --name without-flags | jq -er '.uuid')"
printf '%s\n' '{"flags":{"snapshot":true}}' | cli flags set >"$E2E_RUNTIME/flags-set.json"
jq -e '.flags.snapshot == true' "$E2E_RUNTIME/flags-set.json" >/dev/null || fail "flags set response is wrong"
jq -e '.flags.snapshot == true' "$ALMAGEST_E2E_FILE_STORAGE/flags.json" >/dev/null || fail "flags file was not written"
cli snapshots restore "$empty_snapshot_uuid" >/dev/null
cli flags get | jq -e '.flags.snapshot == true' >/dev/null || fail "snapshot without flags changed live state"
jq -e '.flags.snapshot == true' "$ALMAGEST_E2E_FILE_STORAGE/flags.json" >/dev/null || fail "snapshot without flags changed live file"

snapshot_uuid="$(cli snapshots create --name flags | jq -er '.uuid')"
cli snapshots export --output "$E2E_RUNTIME/flags-export.zip" "$snapshot_uuid" >/dev/null
python3 - "$ALMAGEST_E2E_FILE_STORAGE/snapshots/$snapshot_uuid.zip" <<'PY'
import json
import sys
import zipfile

with zipfile.ZipFile(sys.argv[1]) as archive:
    document = json.loads(archive.read("files/flags.json"))
    if document != {"flags": {"snapshot": True}}:
        raise SystemExit("snapshot omitted flags.json")
PY

printf '%s\n' '{"flags":{"mutated":true}}' | cli flags set >/dev/null
cli flags get | jq -e '.flags.mutated == true and (.flags.snapshot == null)' >/dev/null || fail "flags mutation was not active"
cli snapshots restore "$snapshot_uuid" >/dev/null
cli flags get | jq -e '.flags.snapshot == true and (.flags.mutated == null)' >/dev/null || fail "live restore did not reload flags"

stop_daemon
trap cleanup EXIT
printf '%s\n' '{"flags":{"startup":true}}' >"$ALMAGEST_E2E_FILE_STORAGE/flags.json"
start_daemon
cli flags get | jq -e '.flags.startup == true and (.flags.snapshot == null)' >/dev/null || fail "restart did not parse flags file"

stop_daemon
trap cleanup EXIT
export ALMAGEST_E2E_STARTUP_SNAPSHOT="$E2E_RUNTIME/flags-export.zip"
start_daemon
cli flags get | jq -e '.flags.snapshot == true and (.flags.startup == null)' >/dev/null || fail "startup snapshot did not restore flags"

stop_daemon
trap cleanup EXIT
export ALMAGEST_E2E_STARTUP_SNAPSHOT=""
rm "$ALMAGEST_E2E_FILE_STORAGE/flags.json"
start_daemon
cli flags get | jq -e '. == {}' >/dev/null || fail "restart failed without flags file"

stop_daemon
trap cleanup EXIT
cat >"$E2E_CONFIG_DIR/protected_alm.hujson" <<EOF
{"Admin":{"Unix":"$E2E_ADMIN_SOCK"},"Sandbox":{"Mode":"${ALMAGEST_E2E_SANDBOX_MODE:-auto}"},"Flags":{"fixed":false,"protected":true,"disabled":true}}
EOF
chmod 0600 "$E2E_CONFIG_DIR/protected_alm.hujson"
start_daemon --flag fixed=true --flag disabled=false --flag cli=true
cli flags get | jq -e '.flags == {fixed: true, protected: true, disabled: false, cli: true}' >/dev/null || \
	fail "overrides were not applied to the missing flags file"

printf '%s\n' '{"flags":{"fixed":false,"protected":false,"disabled":true,"cli":false,"free":true}}' | \
	cli flags set >"$E2E_RUNTIME/flags-overridden.json"
jq -e '.flags == {fixed: true, protected: true, disabled: false, cli: true, free: true}' \
	"$E2E_RUNTIME/flags-overridden.json" >/dev/null || fail "flags set response ignored overrides"
jq -e '.flags == {fixed: true, protected: true, disabled: false, cli: true, free: true}' \
	"$ALMAGEST_E2E_FILE_STORAGE/flags.json" >/dev/null || fail "flags write ignored overrides"

stop_daemon
trap cleanup EXIT
printf '%s\n' '{"flags":{"fixed":false,"protected":false,"disabled":true,"cli":false,"free":true}}' \
	>"$ALMAGEST_E2E_FILE_STORAGE/flags.json"
start_daemon --flag fixed=true --flag disabled=false --flag cli=true
cli flags get | jq -e '.flags == {fixed: true, protected: true, disabled: false, cli: true, free: true}' >/dev/null || \
	fail "flags read ignored overrides"

cleanup
log "case passed"
