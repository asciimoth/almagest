#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

menu_item() {
	local node="$1"
	local block="$2"
	local item="$3"
	cli graph menu --name "$node" | jq -cer \
		--arg block "$block" \
		--arg item "$item" \
		'.blocks[] | select(.id == $block) | .items[] | select(.id == $item)'
}

query_response_is() {
	local status="$1"
	local expected="$2"
	local menu
	menu="$(cli graph menu --name "DNS Query")" || return 1
	jq -e --arg status "$status" --arg expected "$expected" '
		([.blocks[] | select(.id == "result") | .items[]]
		 | (map(select(.id == "status"))[0].text == ("Status: " + $status))
		 and (map(select(.id == "error"))[0].value == "")
		 and (map(select(.id == "response"))[0].value == $expected))
	' <<<"$menu" >/dev/null
}

set_query_field() {
	local node="$1"
	local field="$2"
	local value="$3"
	local message
	message="$(jq -nc \
		--arg menu "NODE${node}MENU" \
		--arg field "$field" \
		--arg value "$value" \
		'{
			type:"field.update",
			menuId:$menu,
			field:{blockId:"request",fieldId:$field},
			value:$value
		}')"
	cli engine send-node-formular-msg --id "$node" --message "$message" >/dev/null
}

query_record() {
	local node="$1"
	local name="$2"
	local type="$3"
	local expected="$4"
	local status="${5:-NOERROR}"
	set_query_field "$node" name "$name"
	set_query_field "$node" type "$type"
	cli graph click-menu-button --id "$node" --block request --button send >/dev/null
	retry "$ALMAGEST_E2E_TIMEOUT" "$name $type response" query_response_is "$status" "$expected" \
		|| fail "$name $type returned an unexpected response"
}

apply_static_config() {
	local records="$1"
	local ttl="$2"
	jq -nc --arg records "$records" --argjson ttl "$ttl" \
		'{config:{records:$records,ttl:$ttl}}' \
		| cli graph apply-menu --name "Static DNS" >/dev/null
}

static_form_error_is() {
	local expected="$1"
	menu_item "Static DNS" config records | jq -e --arg expected "$expected" '
		.status == "error" and (.statusText | contains($expected))
	' >/dev/null
}

apply_invalid_records() {
	local records="$1"
	local expected="$2"
	apply_static_config "$records" 90
	retry "$ALMAGEST_E2E_TIMEOUT" "static DNS form error: $expected" \
		static_form_error_is "$expected" \
		|| fail "StaticDns did not report: $expected"
	query_record "$query" address.records.test A \
		$'QUESTION address.records.test. A\nANSWER address.records.test. A 90 192.0.2.10\nANSWER address.records.test. A 90 192.0.2.11'
}

validate_static_field() {
	local field="$1"
	local value="$2"
	local message
	message="$(jq -nc \
		--arg menu "NODE${static}MENU" \
		--arg field "$field" \
		--argjson value "$value" \
		'{
			type:"field.validate",
			menuId:$menu,
			field:{blockId:"config",fieldId:$field},
			value:$value
		}')"
	cli engine send-node-formular-msg --id "$static" --message "$message" >/dev/null
}

field_status_is() {
	local field="$1"
	local status="$2"
	local expected="$3"
	menu_item "Static DNS" config "$field" | jq -e \
		--arg status "$status" --arg expected "$expected" '
		(.status // "") == $status and ((.statusText // "") | contains($expected))
	' >/dev/null
}

start_daemon

for name in "Static DNS" "Fallback DNS" "DNS Query"; do
	wait_graph_node "$name" || fail "$name node is missing"
done

static="$(node_id_by_name "Static DNS")"
fallback="$(node_id_by_name "Fallback DNS")"
query="$(node_id_by_name "DNS Query")"

wait_node_label "$static" "9 DNS records" \
	|| fail "StaticDns did not load all supported records"

query_record "$query" '  ADDRESS.records.test  ' ' a ' \
	$'QUESTION ADDRESS.records.test. A\nANSWER address.records.test. A 90 192.0.2.10\nANSWER address.records.test. A 90 192.0.2.11'
query_record "$query" ipv6.records.test AAA \
	$'QUESTION ipv6.records.test. AAAA\nANSWER ipv6.records.test. AAAA 90 2001:db8::10'
query_record "$query" alias.records.test CNAME \
	$'QUESTION alias.records.test. CNAME\nANSWER alias.records.test. CNAME 90 target.records.test.'
query_record "$query" zone.records.test NS \
	$'QUESTION zone.records.test. NS\nANSWER zone.records.test. NS 90 ns.records.test.'
query_record "$query" 10.2.0.192.in-addr.arpa PTR \
	$'QUESTION 10.2.0.192.in-addr.arpa. PTR\nANSWER 10.2.0.192.in-addr.arpa. PTR 90 ptr.records.test.'
query_record "$query" mail.records.test MX \
	$'QUESTION mail.records.test. MX\nANSWER mail.records.test. MX 90 20 mx.records.test.'
query_record "$query" text.records.test TXT \
	$'QUESTION text.records.test. TXT\nANSWER text.records.test. TXT 90 hello static dns'
query_record "$query" _service._tcp.records.test SRV \
	$'QUESTION _service._tcp.records.test. SRV\nANSWER _service._tcp.records.test. SRV 90 10 5 8443 service.records.test.'

# A miss is forwarded only while the fallback link is attached.
query_record "$query" fallback.records.test A \
	$'QUESTION fallback.records.test. A\nANSWER fallback.records.test. A 30 198.51.100.7'
fallback_link="$(link_id_by_port_names "Static DNS" Fallback "Fallback DNS" DNS)"
cli graph delete-link --id "$fallback_link" >/dev/null
query_record "$query" fallback.records.test A \
	$'QUESTION fallback.records.test. A' NXDOMAIN
add_link_by_port_name "$static" right Fallback "$fallback" left DNS >/dev/null
query_record "$query" fallback.records.test A \
	$'QUESTION fallback.records.test. A\nANSWER fallback.records.test. A 30 198.51.100.7'

# Rejected forms must expose the parser error and retain the active provider.
apply_invalid_records 'broken' 'record must be NAME TYPE VALUE'
apply_invalid_records 'bad.records.test UNKNOWN value' 'unsupported DNS type'
apply_invalid_records 'bad.records.test A 2001:db8::1' 'a record value must be an IPv4 address'
apply_invalid_records 'bad.records.test AAAA 192.0.2.1' 'aaaa record value must be an IPv6 address'
apply_invalid_records 'bad.records.test MX 10' 'mx record value must be PREF HOST'
apply_invalid_records 'bad.records.test MX 70000 mx.records.test' 'mx preference must be between 0 and 65535'
apply_invalid_records 'bad.records.test SRV 1 2 target.records.test' 'srv record value must be PRIORITY WEIGHT PORT TARGET'
apply_invalid_records 'bad.records.test SRV nope 2 3 target.records.test' 'srv priority must be between 0 and 65535'
apply_invalid_records 'bad.records.test TXT "unterminated' 'unterminated quote'

# Field validation must not apply either candidate value.
validate_static_field records '"validate.records.test A invalid"'
retry "$ALMAGEST_E2E_TIMEOUT" "invalid records field status" \
	field_status_is records error 'a record value must be an IPv4 address' \
	|| fail "record field validation did not report its error"
validate_static_field ttl '-1'
retry "$ALMAGEST_E2E_TIMEOUT" "invalid TTL field status" \
	field_status_is ttl error 'ttl value must be a non-negative integer' \
	|| fail "TTL field validation did not report its error"

# A valid apply clears prior errors, replaces records, and normalizes a
# negative TTL to the default value.
apply_static_config $'replacement.records.test TXT "replacement value"\r\n' -1
wait_node_label "$static" "1 DNS record" \
	|| fail "StaticDns did not apply the replacement record"
retry "$ALMAGEST_E2E_TIMEOUT" "cleared static DNS form error" \
	field_status_is records '' '' \
	|| fail "StaticDns kept an error after a valid apply"
query_record "$query" replacement.records.test TXT \
	$'QUESTION replacement.records.test. TXT\nANSWER replacement.records.test. TXT 60 replacement value'
query_record "$query" address.records.test A \
	$'QUESTION address.records.test. A' NXDOMAIN

log "case passed"
