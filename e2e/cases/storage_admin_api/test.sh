#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

export ALMAGEST_E2E_TMP_FILE_STORAGE=0
export ALMAGEST_E2E_FILE_STORAGE=/var/lib/almagest-e2e-storage-admin

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

secret_matches() {
	local key="$1"
	local expression="$2"
	cli secrets list | jq -e --arg key "$key" \
		".secrets[] | select(.key == \$key) | $expression" >/dev/null
}

dns_key_exists() {
	local cache="$1"
	local key="$2"
	cli dns keys --cache "$cache" | jq -e --arg key "$key" \
		'.exists and any(.keys[]; .key == $key)' >/dev/null
}

start_daemon

for name in "Node One" "Node Two"; do
	wait_graph_node "$name" || fail "initial node $name is missing"
done

# Secret mutations, subscription events, protected-entry enforcement, and
# graph-driven allow-list maintenance.
secret_matches protected-token \
	'.value == "protected-value" and .read_only and .source == "protected" and .nodes == ["Node One"]' \
	|| fail "protected secret does not match its config"

start_watch secrets-watch secrets watch
cli secrets set --key mutable-token --value first-value \
	>"$E2E_RUNTIME/secret-created.json"
wait_watch_event "mutable secret upsert event" \
	'select(.method == "update" and .payload.change.key == "mutable-token" and .payload.change.entry.value == "first-value")'
stop_watch

cli secrets set-nodes --key mutable-token \
	--node "Node Two" --node "Node One" --node "Node Two" \
	>"$E2E_RUNTIME/secret-nodes.json"
secret_matches mutable-token \
	'.value == "first-value" and .source == "mutable" and (.read_only | not) and .nodes == ["Node One", "Node Two"]' \
	|| fail "mutable secret nodes were not normalized"

assert_failure_contains secret-unknown-node "secret allows unknown node" \
	cli secrets set-nodes --key mutable-token --node "Missing Node"
secret_matches mutable-token '.nodes == ["Node One", "Node Two"]' \
	|| fail "rejected secret node update changed the allow list"

cli secrets set --key conflict-token --value conflict-value >/dev/null
assert_failure_contains secret-rename-conflict "secret name conflict" \
	cli secrets rename --key mutable-token --new-key conflict-token
secret_matches mutable-token '.value == "first-value"' \
	|| fail "rename conflict removed the source secret"
secret_matches conflict-token '.value == "conflict-value"' \
	|| fail "rename conflict changed the destination secret"

cli secrets rename --key mutable-token --new-key retained-token \
	>"$E2E_RUNTIME/secret-renamed.json"
secret_matches retained-token '.value == "first-value" and .nodes == ["Node One", "Node Two"]' \
	|| fail "renamed secret lost its value or nodes"
if cli secrets list | jq -e '.secrets[] | select(.key == "mutable-token")' >/dev/null; then
	fail "old secret key still exists after rename"
fi

for operation in set delete; do
	case "$operation" in
	set)
		assert_failure_contains secret-protected-set "secret is read-only" \
			cli secrets set --key protected-token --value changed
		;;
	delete)
		assert_failure_contains secret-protected-delete "secret is read-only" \
			cli secrets delete --key protected-token
		;;
	esac
done
secret_matches protected-token '.value == "protected-value" and .nodes == ["Node One"]' \
	|| fail "rejected protected-secret mutation changed the secret"

cli engine set-node-name --name "Node One" --new-name "Node Renamed" >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "mutable secret node rename" \
	secret_matches retained-token '.nodes == ["Node Renamed", "Node Two"]' \
	|| fail "mutable secret did not follow the node rename"
retry "$ALMAGEST_E2E_TIMEOUT" "protected secret node rename" \
	secret_matches protected-token '.nodes == ["Node Renamed"]' \
	|| fail "protected secret did not follow the node rename"

cli graph delete-node --name "Node Renamed" >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "mutable secret node deletion" \
	secret_matches retained-token '.nodes == ["Node Two"]' \
	|| fail "mutable secret retained a deleted node"
retry "$ALMAGEST_E2E_TIMEOUT" "protected secret node deletion" \
	secret_matches protected-token '(.nodes // []) == []' \
	|| fail "protected secret retained a deleted node"

cli secrets set --key clear-token --value clear-value >/dev/null
cli secrets set-nodes --key clear-token --node "Node Two" >/dev/null
cli secrets set-nodes --key clear-token --clear-nodes >/dev/null
secret_matches clear-token '(.nodes // []) == []' \
	|| fail "secret node clear did not remove the allow list"
cli secrets delete --key clear-token >/dev/null
cli secrets delete --key clear-token >/dev/null
if cli secrets list | jq -e '.secrets[] | select(.key == "clear-token")' >/dev/null; then
	fail "deleted secret is still listed"
fi
cli secrets delete --key conflict-token >/dev/null

# KV subscriptions and mutations across store, subtree, and value scopes.
cli kv stores | jq -e \
	'.stores == [{"kind":"default"}]' >/dev/null \
	|| fail "initial KV store list is unexpected"

start_watch kv-stores-watch kv watch-stores
cli kv ensure --plugin admin-test --path branch/nested >/dev/null
wait_watch_event "KV store update event" \
	'select(.method == "stores_update") | any(.payload.snapshot.stores[]; .kind == "plugin" and .name == "admin-test")'
stop_watch

start_watch kv-entries-watch kv watch-entries --plugin admin-test --path branch
cli kv set --plugin admin-test --path branch --key alpha --data alpha-value >/dev/null
wait_watch_event "KV entries update event" \
	'select(.method == "entries_update") | any(.payload.snapshot.entries[]; .kind == "value" and .name == "alpha")'
stop_watch

start_watch kv-value-watch kv watch-value --plugin admin-test --path branch --key alpha
cli kv set --plugin admin-test --path branch --key alpha --data updated-value >/dev/null
wait_watch_event "KV value update event" \
	'select(.method == "value_update" and .payload.snapshot.found and .payload.snapshot.value.data == "updated-value")'
cli kv delete --plugin admin-test --path branch --key alpha >/dev/null
wait_watch_event "KV value delete event" \
	'select(.method == "value_update" and (.payload.snapshot.found | not))'
stop_watch

cli kv set --plugin admin-test --path branch/nested \
	--key binary --data AAH/ --encoding base64 --string=false --hidden \
	>"$E2E_RUNTIME/kv-binary.json"
jq -e '
	.found and .value.data == "AAH/" and .value.encoding == "base64"
	and .value.size == 3 and .value.hidden and (.value.string | not)
' "$E2E_RUNTIME/kv-binary.json" >/dev/null \
	|| fail "binary KV value did not round-trip"

assert_failure_contains kv-invalid-base64 "kv: invalid value" \
	cli kv set --plugin admin-test --path branch/nested \
		--key invalid --data '***' --encoding base64 --string=false
cli kv get --plugin admin-test --path branch/nested --key invalid | \
	jq -e '(.found | not)' >/dev/null \
	|| fail "rejected binary KV value was stored"

assert_failure_contains kv-kind-conflict "kv: name is used by a different entry kind" \
	cli kv set --plugin admin-test --path branch --key nested --data collision
cli kv get --plugin admin-test --path branch/nested --key binary | \
	jq -e '.found and .value.data == "AAH/"' >/dev/null \
	|| fail "KV kind conflict changed the existing subtree"
assert_failure_contains kv-root-drop "kv: cannot drop the root subtree" \
	cli kv drop --plugin admin-test

cli kv set --key retained-default --data retained-value >/dev/null
cli kv ensure --plugin admin-test --path disposable/child >/dev/null
cli kv set --plugin admin-test --path disposable/child --key leaf --data leaf-value >/dev/null
cli kv drop --plugin admin-test --path disposable >/dev/null
cli kv entries --plugin admin-test --path disposable | \
	jq -e '(.exists | not) and ((.entries // []) | length == 0)' >/dev/null \
	|| fail "KV subtree drop left data behind"

# DNS subscriptions, supported record forms, replacement, rejection, delete,
# and cache drop behavior.
start_watch dns-caches-watch dns watch-caches
cli dns set --cache watch-cache --name watch.test \
	--type A --ttl 3600 --value 192.0.2.10 >/dev/null
wait_watch_event "DNS cache create event" \
	'select(.method == "caches_update" and .payload.change.name == "watch-cache" and (.payload.change.deleted | not))'
cli dns drop --cache watch-cache >/dev/null
wait_watch_event "DNS cache delete event" \
	'select(.method == "caches_update" and .payload.change.name == "watch-cache" and .payload.change.deleted)'
stop_watch

start_watch dns-keys-watch dns watch-keys --cache watch-keys-cache
dns_keys_watch_key="$(cli dns set --cache watch-keys-cache --name keys.test. \
	--type TXT --ttl 3600 --value watched | jq -er '.key')"
wait_watch_event "DNS key create event" \
	"select(.method == \"keys_update\" and .payload.change.key == \"$dns_keys_watch_key\" and (.payload.change.deleted | not))"
cli dns drop --cache watch-keys-cache >/dev/null
wait_watch_event "DNS key delete event" \
	"select(.method == \"keys_update\" and .payload.change.key == \"$dns_keys_watch_key\" and .payload.change.deleted)"
stop_watch

dns_value_watch_key="$(cli dns set --cache watch-value-cache --name value.test. \
	--type A --ttl 3600 --value 192.0.2.20 | jq -er '.key')"
start_watch dns-value-watch dns watch-value \
	--cache watch-value-cache --key "$dns_value_watch_key"
cli dns set --cache watch-value-cache --key "$dns_value_watch_key" \
	--name value.test. --type A --ttl 7200 --value 192.0.2.21 >/dev/null
wait_watch_event "DNS value update event" \
	"select(.method == \"value_update\" and .payload.key == \"$dns_value_watch_key\" and .payload.found)"
cli dns delete --cache watch-value-cache --key "$dns_value_watch_key" >/dev/null
wait_watch_event "DNS value delete event" \
	"select(.method == \"value_update\" and .payload.key == \"$dns_value_watch_key\" and (.payload.found | not))"
stop_watch

declare -A dns_records=(
	[a]="host.test.|A|192.0.2.30"
	[aaaa]="host.test.|AAAA|2001:db8::30"
	[cname]="alias.test.|CNAME|host.test."
	[ns]="zone.test.|NS|ns.test."
	[ptr]="30.2.0.192.in-addr.arpa.|PTR|host.test."
	[mx]="mail.test.|MX|10 mx.test."
	[txt]="text.test.|TXT|text value"
	[srv]="_service._tcp.test.|SRV|1 2 443 target.test."
)
dns_record_keys=()
for record in a aaaa cname ns ptr mx txt srv; do
	IFS='|' read -r record_name record_type record_value <<<"${dns_records[$record]}"
	key="$(cli dns set --cache record-cache --name "$record_name" \
		--type "$record_type" --ttl 3600 --value "$record_value" | jq -er '.key')"
	dns_record_keys+=("$key")
	dns_key_exists record-cache "$key" \
		|| fail "$record_type DNS record was not listed"
	cli dns get --cache record-cache --key "$key" | \
		jq -e '.found and (.value.json | fromjson | .message.answers | length == 1)' >/dev/null \
		|| fail "$record_type DNS record did not round-trip"
done
[[ "$(printf '%s\n' "${dns_record_keys[@]}" | sort -u | wc -l)" == "8" ]] \
	|| fail "DNS record types did not produce distinct canonical keys"

record_count_before="$(cli dns keys --cache record-cache | jq -er '.keys | length')"
assert_failure_contains dns-invalid-a "A value must be an IPv4 address" \
	cli dns set --cache record-cache --name invalid.test. \
		--type A --ttl 3600 --value 2001:db8::1
[[ "$(cli dns keys --cache record-cache | jq -er '.keys | length')" == "$record_count_before" ]] \
	|| fail "rejected DNS record changed the cache"

old_key="${dns_record_keys[0]}"
replacement_key="$(cli dns set --cache record-cache --key "$old_key" \
	--name host.test. --type AAAA --ttl 7200 --value 2001:db8::31 | jq -er '.key')"
[[ "$replacement_key" != "$old_key" ]] \
	|| fail "DNS type replacement kept the old canonical key"
cli dns get --cache record-cache --key "$old_key" | jq -e '(.found | not)' >/dev/null \
	|| fail "DNS type replacement retained the old key"
dns_key_exists record-cache "$replacement_key" \
	|| fail "DNS type replacement did not store the new key"
cli dns delete --cache record-cache --key "$replacement_key" >/dev/null
cli dns delete --cache record-cache --key "$replacement_key" >/dev/null
cli dns get --cache record-cache --key "$replacement_key" | jq -e '(.found | not)' >/dev/null \
	|| fail "deleted DNS record is still present"
cli dns drop --cache record-cache >/dev/null
cli dns keys --cache record-cache | jq -e '(.exists | not)' >/dev/null \
	|| fail "DNS cache drop left the cache behind"

retained_dns_key="$(cli dns set --cache retained-cache --name retained.test. \
	--type A --ttl 86400 --value 192.0.2.40 | jq -er '.key')"

# A graceful restart must retain database state and autosaved graph changes.
stop_daemon
start_daemon

wait_graph_node "Node Two" || fail "retained node is missing after restart"
if node_id_by_name "Node One" >/dev/null 2>&1 || node_id_by_name "Node Renamed" >/dev/null 2>&1; then
	fail "deleted node returned after restart"
fi
secret_matches retained-token '.value == "first-value" and .nodes == ["Node Two"]' \
	|| fail "mutable secret was not retained across restart"
secret_matches protected-token \
	'.value == "protected-value" and .read_only and ((.nodes // []) == [])' \
	|| fail "protected secret references were not reconciled after restart"
cli kv get --key retained-default | \
	jq -e '.found and .value.data == "retained-value"' >/dev/null \
	|| fail "default KV value was not retained across restart"
cli kv get --plugin admin-test --path branch/nested --key binary | \
	jq -e '.found and .value.data == "AAH/" and .value.encoding == "base64"' >/dev/null \
	|| fail "plugin KV value was not retained across restart"
dns_key_exists retained-cache "$retained_dns_key" \
	|| fail "DNS record was not retained across restart"
cli dns get --cache retained-cache --key "$retained_dns_key" | \
	jq -e '.found and (.value.json | fromjson | .message.answers[0].data == "wAACKA==")' >/dev/null \
	|| fail "retained DNS record data changed across restart"

log "case passed"
