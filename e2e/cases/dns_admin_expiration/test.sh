#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

watch_pid=""
watch_output=""
watch_error=""

assert_failure_contains() {
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

start_watch() {
	local name="$1"
	shift

	watch_output="$E2E_RUNTIME/${name}.json"
	watch_error="$E2E_RUNTIME/${name}.err"
	rm -f "$watch_output" "$watch_error"
	almagest-cli --url "unix://$E2E_ADMIN_SOCK" "$@" \
		>"$watch_output" 2>"$watch_error" &
	watch_pid="$!"
	retry "$ALMAGEST_E2E_TIMEOUT" "$name initial subscription" \
		grep -F '"subscription_id"' "$watch_output" >/dev/null \
		|| fail "$name did not start"
}

wait_watch_event() {
	local description="$1"
	local expression="$2"
	retry "$ALMAGEST_E2E_TIMEOUT" "$description" \
		jq -e "$expression" "$watch_output" >/dev/null \
		|| fail "$description was not received"
}

stop_watch() {
	if [[ -z "$watch_pid" ]]; then
		return
	fi
	kill "$watch_pid" 2>/dev/null || true
	wait "$watch_pid" 2>/dev/null || true
	watch_pid=""
}

menu_text() {
	local node="$1"
	local block="$2"
	local field="$3"
	cli graph menu --name "$node" | jq -er \
		--arg block "$block" \
		--arg field "$field" \
		'
		.blocks[]
		| select(.id == $block)
		| .items[]
		| select(.id == $field)
		| (.value // .text // "")
		'
}

dns_query_has_answer() {
	local answer="$1"
	[[ "$(menu_text "Expiry Query" result status)" == "Status: NOERROR" \
		&& -z "$(menu_text "Expiry Query" result error)" \
		&& "$(menu_text "Expiry Query" result response)" == *"$answer"* ]]
}

dns_query_has_error() {
	local error_text="$1"
	[[ "$(menu_text "Expiry Query" result status)" == "Status: fail" \
		&& "$(menu_text "Expiry Query" result error)" == *"$error_text"* ]]
}

send_query_expect_answer() {
	local answer="$1"
	cli graph click-menu-button --name "Expiry Query" --block request --button send >/dev/null
	retry "$ALMAGEST_E2E_TIMEOUT" "DNS answer $answer" \
		dns_query_has_answer "$answer" \
		|| fail "cache query did not return $answer"
}

send_query_expect_error() {
	local error_text="$1"
	cli graph click-menu-button --name "Expiry Query" --block request --button send >/dev/null
	retry "$ALMAGEST_E2E_TIMEOUT" "DNS error containing $error_text" \
		dns_query_has_error "$error_text" \
		|| fail "cache query did not fail with: $error_text"
}

dns_value_missing() {
	local key="$1"
	cli dns get --cache expiration-cache --key "$key" | \
		jq -e '(.found | not)' >/dev/null
}

dns_key_missing() {
	local key="$1"
	cli dns keys --cache expiration-cache | jq -e --arg key "$key" \
		'all((.keys // [])[]; .key != $key)' >/dev/null
}

start_daemon
wait_graph_node "DNS Cache" || fail "DNS Cache node is missing"
wait_graph_node "Expiry Query" || fail "Expiry Query node is missing"
cache_id="$(node_id_by_name "DNS Cache")"
wait_node_label "$cache_id" "cache-only DNS" \
	|| fail "DnsCache did not enter cache-only mode"

# Learn the canonical key, remove the seed record, and subscribe before the
# short-lived replacement is inserted. This avoids a race during watch setup.
value_key="$(cli dns set --cache expiration-cache --name value-expiry.test. \
	--type A --ttl 3600 --value 192.0.2.50 | jq -er '.key')"
cli dns delete --cache expiration-cache --key "$value_key" >/dev/null
start_watch dns-value-expiry dns watch-value \
	--cache expiration-cache --key "$value_key"
cli dns set --cache expiration-cache --name value-expiry.test. \
	--type A --ttl 2 --value 192.0.2.50 >/dev/null
wait_watch_event "DNS value creation event" \
	'select(.method == "value_update" and .payload.found and .payload.value != null)'
send_query_expect_answer "192.0.2.50"

# Value snapshots lazily remove expired records. The removal must also evict
# the live cache and notify subscribers.
retry "$ALMAGEST_E2E_TIMEOUT" "expired DNS value removal" \
	dns_value_missing "$value_key" \
	|| fail "expired DNS value remained visible"
wait_watch_event "DNS value expiration event" \
	'select(.method == "value_update" and (.payload.found | not) and .payload.value == null)'
stop_watch
send_query_expect_error "no upstream attached"

# Key snapshots use a separate expiry cleanup path.
start_watch dns-key-expiry dns watch-keys --cache expiration-cache
key_expiry_key="$(cli dns set --cache expiration-cache --name key-expiry.test. \
	--type TXT --ttl 2 --value short-lived | jq -er '.key')"
wait_watch_event "DNS key creation event" \
	"select(.method == \"keys_update\" and .payload.change.key == \"$key_expiry_key\" and (.payload.change.deleted | not))"
retry "$ALMAGEST_E2E_TIMEOUT" "expired DNS key removal" \
	dns_key_missing "$key_expiry_key" \
	|| fail "expired DNS key remained visible"
wait_watch_event "DNS key expiration event" \
	"select(.method == \"keys_update\" and .payload.change.key == \"$key_expiry_key\" and .payload.change.deleted)"
stop_watch

# Exercise all accepted DNS class forms exposed by the admin API.
for class in CH HS 65280; do
	class_key="$(cli dns set --cache class-cache --name "class-$class.test." \
		--type TXT --class "$class" --ttl 3600 --value "$class" | jq -er '.key')"
	cli dns get --cache class-cache --key "$class_key" | jq -e --arg class "$class" '
		.found
		and ((.value.json | fromjson | .message.answers[0].class) ==
			(if $class == "CH" then 3 elif $class == "HS" then 4 else 65280 end))
	' >/dev/null || fail "DNS class $class did not round-trip"
done

class_count="$(cli dns keys --cache class-cache | jq -er '.keys | length')"
assert_failure_contains dns-invalid-class 'unsupported DNS class "INVALID"' \
	cli dns set --cache class-cache --name invalid-class.test. \
		--type TXT --class INVALID --ttl 3600 --value rejected
assert_failure_contains dns-invalid-mx 'MX value must be PREF HOST' \
	cli dns set --cache class-cache --name invalid-mx.test. \
		--type MX --ttl 3600 --value missing-preference
assert_failure_contains dns-invalid-srv 'SRV port must be between 0 and 65535' \
	cli dns set --cache class-cache --name invalid-srv.test. \
		--type SRV --ttl 3600 --value '1 2 65536 target.test.'
[[ "$(cli dns keys --cache class-cache | jq -er '.keys | length')" == "$class_count" ]] \
	|| fail "rejected DNS records changed the class cache"

log "case passed"
