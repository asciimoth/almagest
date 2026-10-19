#!/usr/bin/env bash
set -euo pipefail
source /e2e/lib/common.sh
export ALMAGEST_SUBPROGRAM_LAUNCHER=/usr/local/bin/almagest-subprogram-launcher

assert_native_rejection() {
    local expected=$1
    shift
    local status
    status=$(mktemp)
    if "$ALMAGEST_SUBPROGRAM_LAUNCHER" "$@" 4>"$status"; then
        fail "native launcher accepted invalid input: $expected"
    fi
    [[ $(<"$status") == "$expected: Invalid argument" ]] \
        || fail "native launcher rejection for $expected was not fail-closed"
    rm -f "$status"
}

# These values used to narrow to valid 32-bit identities or counts. Check the
# native trust boundary directly, before any inherited descriptor is used.
assert_native_rejection "non-root identity bounds" \
    worker-v3 4294967297 1 1 1 0 1 /tmp '' -- /program
assert_native_rejection "worker program count" \
    worker-v3 1 1 1 4294967297 0 1 /tmp '' -- /program
assert_native_rejection "launcher memory limit" \
    launch-v4 1 1 32 1099511627777 1 /program /tmp 1 1 --
assert_native_rejection "launcher executable count" \
    v3 1 1 32 33554432 1 1 4294967297 0 '' --

if [[ "$ALMAGEST_E2E_ISOLATION_MODE/$ALMAGEST_E2E_SANDBOX_MODE" == "off/off" ]]; then
    for name in sandbox subprogram-policy subprogram-manager subprogram-direct subprogram-service; do
        "/qualification/$name.test" -test.v -test.timeout=60s
        runuser -u nobody -- "/qualification/$name.test" -test.v -test.timeout=60s
    done
fi
start_daemon
wait_log_contains "almagest/SandboxProbe" "sandbox probe passed: fixed command" \
    || fail "sandbox probe did not start its configured process"
cli admin hello --client-name subprogram-contracts >/tmp/subprogram-hello.json
jq -e --arg sandbox "$ALMAGEST_E2E_SANDBOX_MODE" \
    --arg isolation "$ALMAGEST_E2E_ISOLATION_MODE" '
    .subprogram_sandbox.mode == $sandbox
    and .subprogram_sandbox.platform == "linux"
    and (if $isolation == "off" then
        .execution_mode == "direct" and .isolation == null
    else
        .execution_mode == "isolation" and .isolation.requested_policy == $isolation
    end)
    and (if $sandbox == "off" then
        .subprogram_sandbox.templates.qualification.profile == "direct-v1"
        and (.subprogram_sandbox.templates.qualification.strict_satisfied | not)
        and (.subprogram_sandbox.templates.qualification.full_boundary | not)
        and (.subprogram_sandbox.templates.qualification.features | length > 0)
        and all(.subprogram_sandbox.templates.qualification.features[]; .state == "off")
    else
        .subprogram_sandbox.templates.qualification.profile == "linux-full-v1"
        and .subprogram_sandbox.templates.qualification.strict_satisfied
        and .subprogram_sandbox.templates.qualification.full_boundary
        and .subprogram_sandbox.templates.qualification.effective_identity == "65534:65534"
        and (.subprogram_sandbox.templates.qualification.features | length > 0)
        and all(.subprogram_sandbox.templates.qualification.features[]; .state == "applied")
    end)' \
    /tmp/subprogram-hello.json >/dev/null
log "case passed"
