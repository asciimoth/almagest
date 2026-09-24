#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

raw() {
	local surface="$1"
	local method="$2"
	local payload="${3-}"
	if [[ -z "$payload" ]]; then
		payload='{}'
	fi
	cli raw --surface "$surface" --method "$method" --payload "$payload"
}

expect_failure() {
	local description="$1"
	local surface="$2"
	local method="$3"
	local payload="$4"
	if raw "$surface" "$method" "$payload" \
		>"$E2E_RUNTIME/$description.out" 2>"$E2E_RUNTIME/$description.err"; then
		fail "$description unexpectedly succeeded"
	fi
}

start_daemon

# Secrets: lifecycle, node validation, subscription setup/teardown, and name
# conflicts. Each raw call uses a new session, so subscriptions also exercise
# automatic session cleanup.
raw secrets snapshot >/dev/null
raw secrets subscribe >/dev/null
raw secrets unsubscribe '{"subscription_id":999}' >/dev/null
raw secrets set_value '{"key":"surface-secret","value":"one"}' >/dev/null
raw secrets set_nodes '{"key":"surface-secret","nodes":["Surface Node","Surface Node"]}' >/dev/null
raw secrets rename '{"key":"surface-secret","new_key":"surface-renamed"}' >/dev/null
raw secrets set_value '{"key":"occupied","value":"two"}' >/dev/null
expect_failure secret-rename-conflict secrets rename \
	'{"key":"surface-renamed","new_key":"occupied"}'
expect_failure secret-unknown-node secrets set_nodes \
	'{"key":"surface-renamed","nodes":["Missing Node"]}'
expect_failure secret-empty-key secrets set_value '{"key":"","value":"x"}'
expect_failure secret-invalid-payload secrets set_value '"wrong-type"'
raw secrets set_nodes '{"key":"nodes-only","nodes":[]}' >/dev/null
raw secrets delete '{"key":"nodes-only"}' >/dev/null
raw secrets delete '{"key":"missing"}' >/dev/null
raw secrets delete '{"key":"surface-renamed"}' >/dev/null
raw secrets delete '{"key":"occupied"}' >/dev/null

# KV: default and plugin stores, nested buckets, text aliases, binary values,
# snapshots, subscriptions, invalid selectors, and idempotent cleanup.
raw kv stores_snapshot >/dev/null
raw kv subscribe_stores >/dev/null
raw kv unsubscribe '{"subscription_id":999}' >/dev/null
raw kv ensure '{"path":["surface","nested"]}' >/dev/null
raw kv ensure '{"store":{"kind":"plugin","name":"surface-plugin"},"path":["root"]}' >/dev/null
for encoding in string utf8 plain; do
	raw kv set "{\"path\":[\"surface\",\"nested\"],\"key\":\"$encoding\",\"value\":{\"data\":\"value-$encoding\",\"encoding\":\"$encoding\",\"string\":true}}" >/dev/null
done
raw kv set '{"path":["surface","nested"],"key":"binary","value":{"data":"AAEC/w==","encoding":"base64","hidden":true}}' >/dev/null
raw kv set '{"store":{"kind":"plugin","name":"surface-plugin"},"path":["root"],"key":"value","value":{"data":"cGx1Z2lu","encoding":"base64"}}' >/dev/null
raw kv entries_snapshot '{"path":["surface"]}' >/dev/null
raw kv subscribe_entries '{"path":["surface"]}' >/dev/null
raw kv value_snapshot '{"path":["surface","nested"],"key":"binary"}' >/dev/null
raw kv subscribe_value '{"path":["surface","nested"],"key":"binary"}' >/dev/null
raw kv subscribe_entries '{"path":[""]}' | jq -e '.subscription_id == 0' >/dev/null
raw kv subscribe_value '{"path":[],"key":""}' | jq -e '.subscription_id == 0' >/dev/null
for command in \
	'entries_snapshot {"path":[""]}' \
	'value_snapshot {"path":[],"key":""}' \
	'ensure {"store":{"kind":"unknown"},"path":[]}' \
	'ensure {"store":{"kind":"default","name":"invalid"},"path":[]}' \
	'ensure {"store":{"kind":"plugin","name":""},"path":[]}' \
	'set {"path":[],"key":"bad-base64","value":{"data":"!","encoding":"base64"}}' \
	'set {"path":[],"key":"bad-encoding","value":{"data":"x","encoding":"unknown"}}'; do
	method="${command%% *}"
	payload="${command#* }"
	expect_failure "kv-invalid-$method-$RANDOM" kv "$method" "$payload"
done
raw kv set '{"path":[],"key":"slash/name","value":{"data":"eA=="}}' >/dev/null
raw kv delete '{"path":[],"key":"slash/name"}' >/dev/null
raw kv delete '{"path":["surface","nested"],"key":"binary"}' >/dev/null
raw kv delete '{"path":["surface","nested"],"key":"missing"}' >/dev/null
raw kv drop '{"path":["surface"]}' >/dev/null
raw kv drop '{"store":{"kind":"plugin","name":"surface-plugin"},"path":["root"]}' >/dev/null

# DNS: every supported record and class parser, key replacement, snapshots,
# subscriptions, deletes, drops, and malformed resource data.
raw dns caches_snapshot >/dev/null
raw dns subscribe_caches >/dev/null
raw dns unsubscribe '{"subscription_id":999}' >/dev/null
dns_set() {
	local name="$1" type="$2" value="$3" class="${4:-IN}"
	local payload
	payload="$(jq -nc --arg name "$name" --arg type "$type" --arg value "$value" --arg class "$class" \
		'{cache:"surface",record:{name:$name,type:$type,class:$class,ttl:60,value:$value}}')"
	raw dns set "$payload"
}
a_key="$(dns_set address.surface.test A 192.0.2.1 | jq -er '.key')"
dns_set ipv6.surface.test AAA 2001:db8::1 CH >/dev/null
dns_set alias.surface.test CNAME address.surface.test HS >/dev/null
dns_set zone.surface.test NS ns.surface.test 255 >/dev/null
dns_set reverse.surface.test PTR address.surface.test >/dev/null
dns_set mail.surface.test MX '10 mx.surface.test' >/dev/null
dns_set text.surface.test TXT 'surface text' >/dev/null
dns_set _svc._tcp.surface.test SRV '1 2 8443 target.surface.test' >/dev/null
raw dns keys_snapshot '{"cache":"surface"}' >/dev/null
raw dns subscribe_keys '{"cache":"surface"}' >/dev/null
raw dns value_snapshot "{\"cache\":\"surface\",\"key\":\"$a_key\"}" >/dev/null
raw dns subscribe_value "{\"cache\":\"surface\",\"key\":\"$a_key\"}" >/dev/null
raw dns subscribe_keys '{"cache":""}' | jq -e '.subscription_id == 0' >/dev/null
raw dns subscribe_value '{"cache":"surface","key":""}' | jq -e '.subscription_id == 0' >/dev/null
replacement="$(jq -nc --arg key "$a_key" '{cache:"surface",key:$key,record:{name:"renamed.surface.test",type:"A",ttl:60,value:"192.0.2.2"}}')"
new_key="$(raw dns set "$replacement" | jq -er '.key')"
for record in \
	'{"name":"x.test","type":"UNKNOWN","ttl":1,"value":"x"}' \
	'{"name":"x.test","type":"A","ttl":1,"value":"2001:db8::1"}' \
	'{"name":"x.test","type":"AAAA","ttl":1,"value":"192.0.2.1"}' \
	'{"name":"x.test","type":"MX","ttl":1,"value":"bad"}' \
	'{"name":"x.test","type":"MX","ttl":1,"value":"70000 mx.test"}' \
	'{"name":"x.test","type":"SRV","ttl":1,"value":"bad"}' \
	'{"name":"x.test","type":"SRV","ttl":1,"value":"1 2 70000 x.test"}' \
	'{"name":"x.test","type":"A","class":"bad","ttl":1,"value":"192.0.2.1"}' \
	'{"name":"x.test","type":"A","class":"70000","ttl":1,"value":"192.0.2.1"}' \
	'{"name":"x.test","type":"A","ttl":0,"value":"192.0.2.1"}' \
	'{"name":"","type":"TXT","ttl":1,"value":"x"}'; do
	payload="$(jq -nc --argjson record "$record" '{cache:"surface",record:$record}')"
	expect_failure "dns-invalid-$RANDOM" dns set "$payload"
done
expect_failure dns-invalid-cache dns keys_snapshot '{"cache":""}'
raw dns delete "{\"cache\":\"surface\",\"key\":\"$new_key\"}" >/dev/null
raw dns delete '{"cache":"surface","key":"missing"}' >/dev/null
raw dns drop '{"cache":"surface"}' >/dev/null
raw dns drop '{"cache":"missing"}' >/dev/null

# TLS: certificate and policy lifecycle, both pin kinds, all scope kinds,
# normalization, built-in CA regeneration options, and compile failures.
pem="$(jq -Rs . <"$E2E_CONFIG_DIR/ca.pem")"
raw tls snapshot >/dev/null
raw tls subscribe >/dev/null
raw tls unsubscribe '{"subscription_id":999}' >/dev/null
raw tls builtin_ca >/dev/null
raw tls reload_os_certs >/dev/null
raw tls set_cert "{\"name\":\"surface-ca\",\"pem\":$pem}" >/dev/null
fingerprint="$(raw tls snapshot | jq -er '.certs[] | select(.name == "surface-ca") | .fingerprint')"
policy="$(jq -nc --arg fp "$fingerprint" '{
	name:"surface-policy",
	spec:{
		trust_anchors:[
			{cert:"surface-ca",scope:{dns:[{domain:"example.test",include_subdomains:true}]}},
			{cert:"surface-ca",scope:{ip_prefixes:["192.0.2.1","2001:db8::/32"]}}
		],
		constraints:[
			{cert:"surface-ca",match:"certificate",scope:{any:true}},
			{cert:"surface-ca",match:"spki",scope:{dns:[{domain:"service.test"}]}}
		],
		pins:[
			{kind:"certificate",fingerprint:$fp,scope:{dns:[{domain:"pin.test"}]}},
			{kind:"spki",fingerprint:$fp,scope:{ip_prefixes:["198.51.100.0/24"]}},
			{kind:" CERTIFICATE ",fingerprint:($fp | ascii_upcase),scope:{dns:[{domain:"pin.test",include_subdomains:true}]}}
		]
	}
}')"
raw tls set_policy "$policy" >/dev/null
raw tls rename_cert '{"name":"surface-ca","new_name":"surface-ca-renamed"}' >/dev/null
raw tls rename_policy '{"name":"surface-policy","new_name":"surface-policy-renamed"}' >/dev/null
for spec in \
	'{"trust_anchors":[{"cert":"missing","scope":{"any":true}}]}' \
	'{"trust_anchors":[{"cert":"surface-ca-renamed","scope":{"any":true,"dns":[{"domain":"must-reject.test"}]}}]}' \
	'{"constraints":[{"cert":"surface-ca-renamed","match":"certificate","scope":{"any":true,"ip_prefixes":["192.0.2.0/24"]}}]}' \
	'{"pins":[{"kind":"certificate","fingerprint":"0000000000000000000000000000000000000000000000000000000000000000","scope":{"any":true,"dns":[{"domain":"must-reject.test"}]}}]}' \
	'{"constraints":[{"cert":"surface-ca-renamed","match":"bad","scope":{"any":true}}]}' \
	'{"pins":[{"kind":"bad","fingerprint":"00","scope":{"any":true}}]}' \
	'{"pins":[{"kind":"certificate","fingerprint":"bad","scope":{"any":true}}]}' \
	'{"pins":[{"kind":"certificate","fingerprint":"0000000000000000000000000000000000000000000000000000000000000000","scope":{"ip_prefixes":["bad"]}}]}'; do
	payload="$(jq -nc --argjson spec "$spec" '{name:"rejected-policy",spec:$spec}')"
	expect_failure "tls-invalid-policy-$RANDOM" tls set_policy "$payload"
done
expect_failure tls-invalid-cert tls set_cert '{"name":"bad-cert","pem":"not pem"}'
expect_failure tls-invalid-name tls set_cert "{\"name\":\"\",\"pem\":$pem}"
raw tls delete_cert '{"name":"surface-ca-renamed"}' >/dev/null
raw tls delete_policy '{"name":"surface-policy-renamed"}' >/dev/null
raw tls set_builtin_ca_options \
	'{"options":{"issuer":"Surface E2E","validity_days":2,"key_algorithm":"ecdsa","curve":"P384"}}' >/dev/null
raw tls set_builtin_ca_options \
	'{"options":{"issuer":"Surface E2E RSA","validity_days":2,"key_algorithm":"rsa","key_bits":2048}}' >/dev/null
expect_failure tls-invalid-builtin tls set_builtin_ca_options \
	'{"options":{"issuer":"","validity_days":-1,"key_algorithm":"bad"}}'

cli admin ping --nonce admin-storage-surface | jq -e '.nonce == "admin-storage-surface"' >/dev/null \
	|| fail "daemon did not remain responsive"
log "case passed"
