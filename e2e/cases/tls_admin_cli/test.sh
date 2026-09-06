#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

export SSL_CERT_FILE="$E2E_CONFIG_DIR/os-bundle.pem"
export SSL_CERT_DIR="$E2E_CONFIG_DIR/empty-certs"
ALMAGEST_E2E_TMP_FILE_STORAGE=0
ALMAGEST_E2E_FILE_STORAGE="/root/almagest-e2e-store/$E2E_CASE"

assert_jq() {
	local file="$1"
	local description="$2"
	local filter="$3"

	jq -e "$filter" "$file" >/dev/null || fail "$description"
}

expect_cli_failure() {
	local description="$1"
	local pattern="$2"
	shift 2

	if cli "$@" >"$E2E_RUNTIME/unexpected-success.json" 2>"$E2E_RUNTIME/expected-failure.txt"; then
		fail "$description unexpectedly succeeded"
	fi
	grep -E "$pattern" "$E2E_RUNTIME/expected-failure.txt" >/dev/null \
		|| fail "$description did not report $pattern"
}

start_daemon

cli tls builtin-ca >"$E2E_RUNTIME/builtin-initial.json"
assert_jq "$E2E_RUNTIME/builtin-initial.json" "initial built-in CA was not exposed as a read-only builtin cert" '
	.cert.name == "builtin-ca:almagest"
	and .cert.read_only == true
	and .cert.source == "builtin"
	and .options.issuer == "Almagest"
	and .options.key_algorithm == "ecdsa"
	and .options.curve == "P256"
'
if grep -F "PRIVATE KEY" "$E2E_RUNTIME/builtin-initial.json" >/dev/null; then
	fail "built-in CA API response leaked a private key"
fi

for file in ca.pem ca-key.pem state.json; do
	path="$ALMAGEST_E2E_FILE_STORAGE/tls-builtin-ca/$file"
	[[ -s "$path" ]] || fail "missing built-in CA storage file $file"
	mode="$(stat -c '%a' "$path")"
	[[ "$mode" == "600" ]] || fail "built-in CA storage file $file has mode $mode, want 600"
done

cli tls list >"$E2E_RUNTIME/snapshot-initial.json"
assert_jq "$E2E_RUNTIME/snapshot-initial.json" "protected TLS certificate was not present and read-only" '
	any(.certs[]; .name == "protected-ca" and .source == "protected" and .read_only == true)
'
assert_jq "$E2E_RUNTIME/snapshot-initial.json" "protected TLS policy was not present and read-only" '
	any(.policies[]; .name == "protected-policy" and .source == "protected" and .read_only == true and .valid == true)
'
assert_jq "$E2E_RUNTIME/snapshot-initial.json" "default TLS policy did not include the built-in CA anchor" '
	any(.policies[] | select(.name == "default").spec.trust_anchors[]; .cert == "builtin-ca:almagest" and .scope.any == true)
'

expect_cli_failure "delete protected TLS cert" "conflict: tls config is read-only" \
	tls delete-cert --name protected-ca
expect_cli_failure "replace protected TLS policy" "conflict: tls config is read-only" \
	tls set-policy --name protected-policy --spec '{"trust_anchors":[]}'
expect_cli_failure "set reserved OS certificate name" "bad_request: invalid tls config name" \
	tls set-cert --name os-ca:e2e --pem-file "$E2E_CONFIG_DIR/manual-ca.pem"
expect_cli_failure "set policy with missing certificate" "bad_request: invalid tls config value: tls policy trust anchor [0-9]+ references unknown cert" \
	tls set-policy --name broken-policy --spec '{"trust_anchors":[{"cert":"missing-ca","scope":{"any":true}}]}'

cli tls set-cert --name manual-ca --pem-file "$E2E_CONFIG_DIR/manual-ca.pem" >"$E2E_RUNTIME/manual-cert.json"
assert_jq "$E2E_RUNTIME/manual-cert.json" "manual certificate was not saved as mutable" '
	.name == "manual-ca" and .source == "mutable" and .read_only != true and .is_ca == true
'

cat >"$E2E_RUNTIME/manual-policy.json" <<'JSON'
{
  "trust_anchors": [
    {
      "cert": "manual-ca",
      "scope": {
        "dns": [
          {"domain": "manual.example.test", "include_subdomains": true}
        ]
      }
    }
  ],
  "constraints": [
    {
      "cert": "protected-ca",
      "match": "spki",
      "scope": {"ip_prefixes": ["127.0.0.1"]}
    }
  ]
}
JSON
cli tls set-policy --name manual-policy --spec-file "$E2E_RUNTIME/manual-policy.json" >"$E2E_RUNTIME/manual-policy-set.json"
assert_jq "$E2E_RUNTIME/manual-policy-set.json" "manual policy did not compile with manual and built-in anchors" '
	.name == "manual-policy"
	and .source == "mutable"
	and .valid == true
	and any(.spec.trust_anchors[]; .cert == "manual-ca" and any(.scope.dns[]?; .domain == "manual.example.test" and .include_subdomains == true))
	and any(.spec.trust_anchors[]; .cert == "builtin-ca:almagest" and .scope.any == true)
	and any(.spec.constraints[]; .cert == "protected-ca" and .match == "spki" and any(.scope.ip_prefixes[]?; . == "127.0.0.1"))
'

cli tls rename-cert --name manual-ca --new-name renamed-ca >"$E2E_RUNTIME/renamed-cert.json"
cli tls list >"$E2E_RUNTIME/snapshot-renamed.json"
assert_jq "$E2E_RUNTIME/snapshot-renamed.json" "renamed certificate was not present" '
	any(.certs[]; .name == "renamed-ca" and .source == "mutable")
'
# Dollar-prefixed names in this expression are jq variables.
# shellcheck disable=SC2016
assert_jq "$E2E_RUNTIME/snapshot-renamed.json" "policy did not rewrite certificate reference after rename" '
	(.policies[] | select(.name == "manual-policy")) as $p
	| any($p.spec.trust_anchors[]; .cert == "renamed-ca")
	and ([ $p.spec.trust_anchors[]? | select(.cert == "manual-ca") ] | length == 0)
'

cli tls set-cert --name persist-ca --pem-file "$E2E_CONFIG_DIR/persist-ca.pem" >"$E2E_RUNTIME/persist-cert.json"
cli tls set-policy --name persist-policy --spec '{"trust_anchors":[{"cert":"persist-ca","scope":{"any":true}}]}' >"$E2E_RUNTIME/persist-policy.json"

cli tls delete-cert --name renamed-ca >"$E2E_RUNTIME/delete-renamed.json"
cli tls list >"$E2E_RUNTIME/snapshot-deleted.json"
assert_jq "$E2E_RUNTIME/snapshot-deleted.json" "deleted certificate was still present" '
	([ .certs[] | select(.name == "renamed-ca") ] | length) == 0
'
# Dollar-prefixed names in this expression are jq variables.
# shellcheck disable=SC2016
assert_jq "$E2E_RUNTIME/snapshot-deleted.json" "policy did not remove deleted certificate reference" '
	(.policies[] | select(.name == "manual-policy")) as $p
	| $p.valid == true
	and ([ $p.spec.trust_anchors[]? | select(.cert == "renamed-ca") ] | length == 0)
	and any($p.spec.trust_anchors[]; .cert == "builtin-ca:almagest")
'

initial_builtin_fingerprint="$(jq -er '.cert.fingerprint' "$E2E_RUNTIME/builtin-initial.json")"
cli tls set-builtin-ca \
	--issuer "E2E Changed Builtin" \
	--validity-days 30 \
	--key-algorithm rsa \
	--key-bits 2048 \
	>"$E2E_RUNTIME/builtin-changed.json"
changed_builtin_fingerprint="$(jq -er '.cert.fingerprint' "$E2E_RUNTIME/builtin-changed.json")"
[[ "$changed_builtin_fingerprint" != "$initial_builtin_fingerprint" ]] \
	|| fail "built-in CA fingerprint did not change after option update"
assert_jq "$E2E_RUNTIME/builtin-changed.json" "built-in CA options update did not return normalized RSA options" '
	.options.issuer == "E2E Changed Builtin"
	and .options.validity_days == 30
	and .options.key_algorithm == "rsa"
	and .options.key_bits == 2048
'
if grep -F "PRIVATE KEY" "$E2E_RUNTIME/builtin-changed.json" >/dev/null; then
	fail "changed built-in CA API response leaked a private key"
fi
retry "$ALMAGEST_E2E_TIMEOUT" "built-in CA config autosave" \
	grep -F "E2E Changed Builtin" "$E2E_CONFIG_DIR/alm.hujson" >/dev/null \
	|| fail "built-in CA options were not autosaved to config"

stop_daemon
start_daemon

cli tls builtin-ca >"$E2E_RUNTIME/builtin-after-restart.json"
after_restart_fingerprint="$(jq -er '.cert.fingerprint' "$E2E_RUNTIME/builtin-after-restart.json")"
[[ "$after_restart_fingerprint" == "$changed_builtin_fingerprint" ]] \
	|| fail "built-in CA changed across restart despite matching persisted options"
assert_jq "$E2E_RUNTIME/builtin-after-restart.json" "built-in CA options did not survive restart" '
	.options.issuer == "E2E Changed Builtin"
	and .options.key_algorithm == "rsa"
	and .options.key_bits == 2048
'

cli tls list >"$E2E_RUNTIME/snapshot-after-restart.json"
assert_jq "$E2E_RUNTIME/snapshot-after-restart.json" "mutable cert/policy did not survive restart" '
	any(.certs[]; .name == "persist-ca" and .source == "mutable")
	and any(.policies[]; .name == "persist-policy" and .source == "mutable" and .valid == true)
'
assert_jq "$E2E_RUNTIME/snapshot-after-restart.json" "protected entries did not survive restart overlay" '
	any(.certs[]; .name == "protected-ca" and .source == "protected" and .read_only == true)
	and any(.policies[]; .name == "protected-policy" and .source == "protected" and .read_only == true)
'

log "case passed"
