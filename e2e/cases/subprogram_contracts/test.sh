#!/usr/bin/env bash
set -euo pipefail
source /e2e/lib/common.sh
export ALMAGEST_SUBPROGRAM_LAUNCHER=/usr/local/bin/almagest-subprogram-launcher
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
