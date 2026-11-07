#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

export SSL_CERT_FILE="$E2E_CONFIG_DIR/os-bundle.pem"
export SSL_CERT_DIR="$E2E_CONFIG_DIR/empty-certs"

assert_jq() {
	local file="$1"
	local description="$2"
	local filter="$3"

	jq -e "$filter" "$file" >/dev/null || fail "$description"
}

json_value() {
	local file="$1"
	local filter="$2"

	jq -er "$filter" "$file"
}

watch_pid=""
stop_watch() {
	if [[ -n "$watch_pid" ]]; then
		kill "$watch_pid" 2>/dev/null || true
		wait "$watch_pid" 2>/dev/null || true
		watch_pid=""
	fi
}

start_daemon

cli tls list >"$E2E_RUNTIME/snapshot-initial.json"
initial_name="$(json_value "$E2E_RUNTIME/snapshot-initial.json" '.certs[] | select(.subject | contains("CN=E2E Reload Initial Root")) | .name')"
warning_name="$(json_value "$E2E_RUNTIME/snapshot-initial.json" '.certs[] | select(any(.certcat_matches[]?; .action == "warn")) | .name')"
narrow_name="$(json_value "$E2E_RUNTIME/snapshot-initial.json" '.certs[] | select(any(.certcat_matches[]?; .action == "narrow_scope")) | .name')"

assert_jq "$E2E_RUNTIME/snapshot-initial.json" "invalid OS certificate was loaded" '
	[.certs[] | select(.subject | contains("CN=E2E Invalid Serial Root"))] | length == 0
'
grep -E '\[WARN\].*TLS certificates: skipping invalid OS authority.*negative serial number' \
	"$E2E_DAEMON_LOG" >/dev/null \
	|| fail "daemon did not warn about the skipped negative-serial certificate"

assert_jq "$E2E_RUNTIME/snapshot-initial.json" "initial OS root was not present" '
	any(.certs[]; .name == "'"$initial_name"'" and .source == "os" and .read_only == true)
'
assert_jq "$E2E_RUNTIME/snapshot-initial.json" "warning certcat root was not annotated" '
	any(.certs[]; .name == "'"$warning_name"'" and any(.certcat_matches[]?; .action == "warn" and .severity == "warning"))
'
assert_jq "$E2E_RUNTIME/snapshot-initial.json" "narrow certcat root was not annotated" '
	any(.certs[]; .name == "'"$narrow_name"'" and any(.certcat_matches[]?; .action == "narrow_scope"))
'
assert_jq "$E2E_RUNTIME/snapshot-initial.json" "default policy did not include the ordinary OS root with any scope" '
	any(.policies[] | select(.name == "default").spec.trust_anchors[]; .cert == "'"$initial_name"'" and .scope.any == true)
'
assert_jq "$E2E_RUNTIME/snapshot-initial.json" "default policy included warning certcat root" '
	([ .policies[] | select(.name == "default").spec.trust_anchors[] | select(.cert == "'"$warning_name"'") ] | length) == 0
'
assert_jq "$E2E_RUNTIME/snapshot-initial.json" "default policy did not include narrow certcat DNS scope" '
	any(.policies[] | select(.name == "default").spec.trust_anchors[]; .cert == "'"$narrow_name"'"
		and any(.scope.dns[]?; .domain == "apps.example.internal" and .include_subdomains == true)
		and any(.scope.dns[]?; .domain == "example.internal" and .include_subdomains != true))
'

watch_file="$E2E_RUNTIME/tls-watch.jsonl"
watch_err="$E2E_RUNTIME/tls-watch.err"
timeout --kill-after=2 15 \
	almagest-cli --url "unix://$E2E_ADMIN_SOCK" tls watch \
	>"$watch_file" 2>"$watch_err" &
watch_pid="$!"
retry "$ALMAGEST_E2E_TIMEOUT" "TLS watch subscription" \
	grep -F '"subscription_id"' "$watch_file" >/dev/null \
	|| fail "TLS watch did not subscribe"

cat \
	"$E2E_CONFIG_DIR/replacement-root.pem" \
	"$E2E_CONFIG_DIR/negative-serial.pem" \
	"$E2E_CONFIG_DIR/fixtures/narrow-scope-ca.pem" \
	>"$E2E_CONFIG_DIR/os-bundle.pem"

cli tls reload-os-certs >"$E2E_RUNTIME/snapshot-reload.json"
replacement_name="$(json_value "$E2E_RUNTIME/snapshot-reload.json" '.certs[] | select(.subject | contains("CN=E2E Reload Replacement Root")) | .name')"
assert_jq "$E2E_RUNTIME/snapshot-reload.json" "invalid OS certificate was loaded after reload" '
	[.certs[] | select(.subject | contains("CN=E2E Invalid Serial Root"))] | length == 0
'

assert_jq "$E2E_RUNTIME/snapshot-reload.json" "old OS root was still present after reload" '
	([ .certs[] | select(.name == "'"$initial_name"'") ] | length) == 0
'
assert_jq "$E2E_RUNTIME/snapshot-reload.json" "warning root was still present after reload" '
	([ .certs[] | select(.name == "'"$warning_name"'") ] | length) == 0
'
assert_jq "$E2E_RUNTIME/snapshot-reload.json" "replacement OS root was not present after reload" '
	any(.certs[]; .name == "'"$replacement_name"'" and .source == "os" and .read_only == true)
'
assert_jq "$E2E_RUNTIME/snapshot-reload.json" "narrow OS root did not survive reload" '
	any(.certs[]; .name == "'"$narrow_name"'" and .source == "os")
'
# Dollar-prefixed names in this expression are jq variables.
# shellcheck disable=SC2016
assert_jq "$E2E_RUNTIME/snapshot-reload.json" "default policy did not rebuild after reload" '
	(.policies[] | select(.name == "default")) as $p
	| $p.source == "os"
	and $p.read_only == true
	and $p.valid == true
	and any($p.spec.trust_anchors[]; .cert == "'"$replacement_name"'" and .scope.any == true)
	and ([ $p.spec.trust_anchors[] | select(.cert == "'"$initial_name"'" or .cert == "'"$warning_name"'") ] | length) == 0
'

# Dollar-prefixed names in this expression are jq variables.
# shellcheck disable=SC2016
retry "$ALMAGEST_E2E_TIMEOUT" "TLS reload watch events" \
	jq -s -e \
		--arg initial "$initial_name" \
		--arg warning "$warning_name" \
		--arg replacement "$replacement_name" \
		'
		any(.[]; .method == "update" and .payload.change.kind == "cert" and .payload.change.name == $initial and .payload.change.deleted == true)
		and any(.[]; .method == "update" and .payload.change.kind == "cert" and .payload.change.name == $warning and .payload.change.deleted == true)
		and any(.[]; .method == "update" and .payload.change.kind == "cert" and .payload.change.name == $replacement and .payload.change.cert.source == "os")
		and any(.[]; .method == "update" and .payload.change.kind == "policy" and .payload.change.name == "default" and .payload.change.policy.valid == true)
		' "$watch_file" >/dev/null \
	|| fail "TLS watch did not receive expected reload events"
stop_watch

assert_daemon_log_contains "TLS OS certificate reload"

log "case passed"
