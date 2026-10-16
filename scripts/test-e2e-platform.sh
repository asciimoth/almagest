#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
if [[ $(go env GOHOSTOS) == windows ]]; then
    exec "$repo_root/scripts/test-windows.sh" privileged
fi

"$repo_root/e2e/lib/case_scheduler_test.sh"
"$repo_root/e2e/run_test.sh"
"$repo_root/e2e/lib/common_test.sh"
"$repo_root/e2e/lib/amnesia_peer_handoff_test.sh"
"$repo_root/scripts/test-e2e-fail-fast_test.sh"
python3 "$repo_root/e2e/lib/udp_echo_client_test.py"
python3 "$repo_root/e2e/lib/configure_sandbox_mode_test.py"
if [[ -n ${E2E_INCLUDE_PRIVILEGED:-} ]]; then
    E2E_COVERAGE=1 E2E_INCLUDE_PRIVILEGED="$E2E_INCLUDE_PRIVILEGED" "$repo_root/e2e/run.sh"
else
    E2E_COVERAGE=1 "$repo_root/e2e/run.sh"
fi
