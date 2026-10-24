#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

start_daemon

cli tls list >"$E2E_RUNTIME/initial.json"
jq -e '
	.revocations.enabled == true
	and (.revocations.system | length) == 1
	and .revocations.system[0].source == "linux:p11-kit"
' "$E2E_RUNTIME/initial.json" >/dev/null || fail "Linux distrust provider status is missing"
jq -e '.revocations.system[0].state == "unsupported" or .revocations.system[0].state == "unavailable"' \
	"$E2E_RUNTIME/initial.json" >/dev/null || fail "Linux distrust provider reported an unsafe active state"

cli tls reload-os-certs >/dev/null
cli tls list >"$E2E_RUNTIME/refreshed.json"
jq -e '
	.revocations.system[0].source == "linux:p11-kit"
' "$E2E_RUNTIME/refreshed.json" >/dev/null || fail "manual refresh discarded platform distrust status"

# The public status contains normalized findings and provider state only.
if grep -Eqi 'registry|pkcs11|\.so|/etc/|/usr/' "$E2E_RUNTIME/refreshed.json"; then
	fail "platform status disclosed a host selector"
fi
