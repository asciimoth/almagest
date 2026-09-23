#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

web_port=18090
web_url="web://127.0.0.1:$web_port"
export ALMAGEST_E2E_WEB_ADMIN="$web_port"
export ALMAGEST_E2E_WEB_PPROF=1

web_cli() {
	timeout --kill-after=2 "$ALMAGEST_E2E_CLI_TIMEOUT" \
		almagest-cli --url "$web_url" "$@"
}

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

acquire_expired_lease() {
	web_cli admin acquire-mutation --holder expiry-successor --steal-expired \
		--ttl-ms 5000 >"$E2E_RUNTIME/lease-expiry-successor.json" \
		&& jq -e '.acquired and .lease.holder == "expiry-successor"' \
			"$E2E_RUNTIME/lease-expiry-successor.json" >/dev/null
}

start_daemon

cli admin hello --client-name unix-e2e --version e2e \
	--surfaces admin,engine >"$E2E_RUNTIME/hello-unix.json"
jq -e '
	.version != ""
	and (.surfaces.admin.methods | index("acquire_mutation") != null)
	and (.surfaces.admin.methods | index("renew_mutation") != null)
	and (.surfaces.admin.methods | index("release_mutation") != null)
	and (.surfaces.engine.methods | index("workspace_snapshot") != null)
' "$E2E_RUNTIME/hello-unix.json" >/dev/null \
	|| fail "Unix API discovery response is incomplete"

cli admin colormap >"$E2E_RUNTIME/colormap.json"
jq -e 'type == "object" and length > 0' "$E2E_RUNTIME/colormap.json" >/dev/null \
	|| fail "admin color map is empty"

retry "$ALMAGEST_E2E_TIMEOUT" "web admin API" \
	web_cli admin ping --nonce web-transport >"$E2E_RUNTIME/web-ping.json" \
	|| fail "web admin API did not answer ping"
jq -e '.nonce == "web-transport"' "$E2E_RUNTIME/web-ping.json" >/dev/null \
	|| fail "web admin API ping returned an unexpected nonce"

web_cli admin hello --client-name web-e2e --version e2e \
	--surfaces admin >"$E2E_RUNTIME/hello-web.json"
jq -e '.surfaces.admin.methods | index("ping") != null' \
	"$E2E_RUNTIME/hello-web.json" >/dev/null \
	|| fail "web API discovery response is incomplete"

curl --fail --silent --show-error "http://127.0.0.1:$web_port/" \
	--output "$E2E_RUNTIME/admin-index.html"
grep -F '<html' "$E2E_RUNTIME/admin-index.html" >/dev/null \
	|| fail "web admin root did not serve the panel"
curl --fail --silent --show-error "http://127.0.0.1:$web_port/debug/pprof/" \
	--output "$E2E_RUNTIME/pprof-index.html"
grep -F 'Types of profiles available' "$E2E_RUNTIME/pprof-index.html" >/dev/null \
	|| fail "web admin pprof index is missing"

cli admin acquire-mutation --holder unix-owner --ttl-ms 5000 \
	>"$E2E_RUNTIME/lease-owner.json"
lease_id="$(jq -er 'select(.acquired) | .lease.id' "$E2E_RUNTIME/lease-owner.json")"
[[ -n "$lease_id" ]] || fail "Unix client did not acquire a mutation lease"

web_cli admin acquire-mutation --holder web-contender --ttl-ms 5000 \
	>"$E2E_RUNTIME/lease-contender.json"
jq -e --arg lease_id "$lease_id" '
	(.acquired | not)
	and .active.id == $lease_id
	and .active.holder == "unix-owner"
' "$E2E_RUNTIME/lease-contender.json" >/dev/null \
	|| fail "web client did not observe the Unix mutation lease"

assert_cli_failure lease-renew-unknown "conflict: mutation lease is not active" \
	web_cli admin renew-mutation --lease-id unknown --ttl-ms 5000

web_cli admin renew-mutation --lease-id "$lease_id" --ttl-ms 10000 \
	>"$E2E_RUNTIME/lease-renewed.json"
jq -e --arg lease_id "$lease_id" \
	'.id == $lease_id and .holder == "unix-owner" and .expires_at != ""' \
	"$E2E_RUNTIME/lease-renewed.json" >/dev/null \
	|| fail "web client did not renew the Unix mutation lease"

web_cli admin release-mutation --lease-id unknown >/dev/null
cli admin acquire-mutation --holder after-wrong-release --ttl-ms 5000 \
	>"$E2E_RUNTIME/lease-after-wrong-release.json"
jq -e --arg lease_id "$lease_id" \
	'(.acquired | not) and .active.id == $lease_id' \
	"$E2E_RUNTIME/lease-after-wrong-release.json" >/dev/null \
	|| fail "an unknown lease ID released the active lease"

web_cli admin release-mutation --lease-id "$lease_id" >/dev/null
cli admin acquire-mutation --holder default-ttl \
	>"$E2E_RUNTIME/lease-default.json"
default_lease_id="$(jq -er 'select(.acquired) | .lease.id' "$E2E_RUNTIME/lease-default.json")"
[[ -n "$default_lease_id" ]] || fail "default-TTL mutation lease was not acquired"
cli admin release-mutation --lease-id "$default_lease_id" >/dev/null

cli admin acquire-mutation --holder expiring --ttl-ms 50 \
	>"$E2E_RUNTIME/lease-expiring.json"
jq -e '.acquired and .lease.holder == "expiring"' \
	"$E2E_RUNTIME/lease-expiring.json" >/dev/null \
	|| fail "short mutation lease was not acquired"
retry "$ALMAGEST_E2E_TIMEOUT" "expired mutation lease takeover" acquire_expired_lease \
	|| fail "expired mutation lease was not replaced"

successor_id="$(jq -er '.lease.id' "$E2E_RUNTIME/lease-expiry-successor.json")"
web_cli admin release-mutation --lease-id "$successor_id" >/dev/null

log "case passed"
