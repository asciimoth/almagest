#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

trusted_dir="$E2E_ISOLATION_ROOT/protected-inputs"
export SSL_CERT_FILE="$trusted_dir/root.pem"
export SSL_CERT_DIR="$E2E_CONFIG_DIR/empty-certs"

start_daemon

cli tls list >"$E2E_RUNTIME/tls.json"
jq -e '
	.revocations.enabled == true
	and any(.revocations.sources[]; .name == "e2e-root-revocations" and .status == "valid" and .source == "protected" and .revoked_count == 1)
	and any(.certs[]; (.subject | contains("CN=E2E Revoked OS Root")) and .source == "os" and (.revocation_matches | length) == 1)
' "$E2E_RUNTIME/tls.json" >/dev/null || fail "revoked OS root did not have CRL status and warning"

root_name="$(jq -er '.certs[] | select(.subject | contains("CN=E2E Revoked OS Root")) | .name' "$E2E_RUNTIME/tls.json")"
jq -e --arg root "$root_name" '
	([.policies[] | select(.name == "default").spec.trust_anchors[]? | select(.cert == $root)] | length) == 0
' "$E2E_RUNTIME/tls.json" >/dev/null || fail "default policy retained a revoked OS root"

cli tls crls >"$E2E_RUNTIME/crls.json"
jq -e '.enabled == true and .sources[0].status == "valid" and (has("pem") | not)' \
	"$E2E_RUNTIME/crls.json" >/dev/null || fail "tls crls disclosed data or omitted status"

# Static files are one-time launch inputs. A replacement must not change the
# running snapshot, in direct or isolated mode.
printf 'invalid replacement\n' >"$trusted_dir/revoked.crl.pem"
cli tls list >"$E2E_RUNTIME/after-replacement.json"
jq -e '.revocations.sources[0].status == "valid" and .revocations.sources[0].revoked_count == 1' \
	"$E2E_RUNTIME/after-replacement.json" >/dev/null || fail "running static CRL changed after file replacement"
