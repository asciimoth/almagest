#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

ALMAGEST_E2E_TIMEOUT="${ALMAGEST_E2E_TIMEOUT:-30}"

upstream_pid=""
upstream_log="$E2E_RUNTIME/upstream-dns.log"

cleanup() {
	if [[ -n "$upstream_pid" ]]; then
		kill "$upstream_pid" 2>/dev/null || true
		wait "$upstream_pid" 2>/dev/null || true
	fi
	stop_daemon
	if [[ -x "$E2E_RUNTIME/cleanup-host.sh" ]]; then
		"$E2E_RUNTIME/cleanup-host.sh"
	fi
}

start_upstream_dns() {
	local conf="$E2E_RUNTIME/upstream-dnsmasq.conf"
	cat >"$conf" <<EOF
port=5453
listen-address=127.0.0.1
bind-interfaces
no-resolv
no-hosts
local-ttl=60
log-queries
log-facility=$upstream_log
address=/example.com/93.184.216.34
EOF
	dnsmasq --no-daemon --conf-file="$conf" >/dev/null 2>"$upstream_log.stderr" &
	upstream_pid="$!"
	retry "$ALMAGEST_E2E_TIMEOUT" "upstream DNS listener" nc -z 127.0.0.1 5453 \
		|| fail "upstream DNS did not listen on 127.0.0.1:5453"
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

dns_query_ready() {
	local node="$1"
	local want="$2"
	local status
	local err
	local resp
	status="$(menu_text "$node" result status)" || return 1
	err="$(menu_text "$node" result error)" || return 1
	resp="$(menu_text "$node" result response)" || return 1
	[[ "$status" == "Status: NOERROR" && -z "$err" && "$resp" == *"$want"* ]]
}

send_dns_query_and_wait() {
	local node="$1"
	local want="$2"
	cli graph click-menu-button --name "$node" --block request --button send >/dev/null
	retry "$ALMAGEST_E2E_TIMEOUT" "$node DNS response containing $want" \
		dns_query_ready "$node" "$want" \
		|| fail "$node did not return DNS response containing $want"
}

http_client_ready() {
	local status
	local err
	local resp
	status="$(menu_text "HttpClient xP" result status)" || return 1
	err="$(menu_text "HttpClient xP" result error)" || return 1
	resp="$(menu_text "HttpClient xP" result response)" || return 1
	[[ "$status" == "Status: 200 OK" && -z "$err" && "$resp" == "Debug http server response" ]]
}

send_http_client_and_wait() {
	cli graph click-menu-button --name "HttpClient xP" --block request --button send >/dev/null
	retry "$ALMAGEST_E2E_TIMEOUT" "HttpClient response from HttpServer" \
		http_client_ready \
		|| fail "HttpClient did not receive the HttpServer response"
}

curl_proxy_to_http_server() {
	curl -g --fail --silent --show-error \
		--socks5-hostname 127.0.0.1:1081 \
		http://example2.test:8080/ \
		--output /tmp/e2e-dns-example-curl.txt
}

assert_curl_proxy_reaches_http_server() {
	retry "$ALMAGEST_E2E_TIMEOUT" "curl through LocalProxy to HttpServer" \
		curl_proxy_to_http_server \
		|| fail "curl through LocalProxy did not reach HttpServer"

	local body
	body="$(cat /tmp/e2e-dns-example-curl.txt)"
	[[ "$body" == "Debug http server response" ]] \
		|| fail "unexpected proxied response body: $body"
}

log_dns_example_request_count() {
	cli log subscribe | jq -er '
		[
			.entries[]
			| select(
				.node_class == "almagest/LogDNS"
				and (.message | contains("dns.Request "))
				and (.message | contains("questions=example.com. A"))
			)
		]
		| length
	'
}

log_dns_example_request_count_is() {
	local want="$1"
	[[ "$(log_dns_example_request_count)" == "$want" ]]
}

assert_log_dns_count_stays() {
	local want="$1"
	local seconds="$2"
	local deadline=$((SECONDS + seconds))
	while ((SECONDS <= deadline)); do
		log_dns_example_request_count_is "$want" || return 1
		sleep 0.25
	done
}

system_dig_answer() {
	local name="$1"
	timeout 5 dig +tries=1 +time=1 +short "$name" A 2>/tmp/e2e-dns-example-dig.err \
		| sed '/^$/d'
}

system_dig_resolves_to() {
	local name="$1"
	local want="$2"
	local got
	got="$(system_dig_answer "$name")" || return 1
	[[ "$got" == "$want" ]]
}

system_dig_has_no_answer() {
	local name="$1"
	local got
	if got="$(system_dig_answer "$name")"; then
		[[ -z "$got" ]]
		return
	fi
	return 0
}

system_ipv6_dig_resolves_to() {
	local name="$1"
	local want="$2"
	local got
	got="$(timeout 5 dig -6 +tries=1 +time=2 +short \
		@2001:4860:4860::8888 "$name" A \
		2>"$E2E_RUNTIME/ipv6-dig.err" | sed '/^$/d')" || return 1
	[[ "$got" == "$want" ]]
}

delete_dns_input_link() {
	local right_node="$1"
	local left_node="$2"
	local link
	link="$(link_id_by_port_names "$right_node" DNS "$left_node" DNS)" \
		|| fail "missing DNS link from $right_node to $left_node"
	cli graph delete-link --id "$link" >/dev/null
}

trap cleanup EXIT

start_upstream_dns
start_daemon
trap cleanup EXIT

for node in \
	"DefaultTun BS" \
	"Tun2Dns LN" \
	"MeshNames xP" \
	"DnsRouter BS" \
	"DnsCache xP" \
	"DnsClient I2" \
	"DnsServer xP" \
	"LogDNS md" \
	"HttpClient xP" \
	"HttpServer LN" \
	"LocalProxy I2" \
	"DnsQuery LN" \
	"DnsQuery I2" \
	"DnsQuery ZG" \
	"DnsQuery HF"; do
	wait_graph_node "$node" || fail "$node node missing"
done

default_tun="$(node_id_by_name "DefaultTun BS")"
tun2dns="$(node_id_by_name "Tun2Dns LN")"
dns_cache="$(node_id_by_name "DnsCache xP")"
dns_client="$(node_id_by_name "DnsClient I2")"
dns_router="$(node_id_by_name "DnsRouter BS")"
mesh_names="$(node_id_by_name "MeshNames xP")"

wait_node_label "$default_tun" ready || fail "DefaultTun did not become ready"
wait_node_label "$tun2dns" "adapting DNS" || fail "Tun2Dns did not become ready"
wait_node_label "$dns_cache" "caching DNS" || fail "DnsCache did not attach upstream DNS"
wait_node_label "$dns_client" "1 DNS server" || fail "DnsClient did not become ready"
wait_node_label "$dns_router" "routing 2 DNS backends" || fail "DnsRouter did not attach both backends"
wait_node_label "$mesh_names" "resolving MeshNames with fallback" || fail "MeshNames did not become ready"

wait_log_contains "almagest/DnsServer" "listening on udp/127.0.0.1:5353" \
	|| fail "DNS server did not start listening"
wait_log_contains "almagest/HttpServer" "listening on 127.0.0.1:8080" \
	|| fail "HTTP server did not start listening"
wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:1081" \
	|| fail "LocalProxy did not start listening"

send_dns_query_and_wait "DnsQuery LN" "93.184.216.34"
retry "$ALMAGEST_E2E_TIMEOUT" "single LogDNS example.com request" \
	log_dns_example_request_count_is 1 \
	|| fail "LogDNS did not record the first global example.com request"

send_dns_query_and_wait "DnsQuery LN" "93.184.216.34"
assert_log_dns_count_stays 1 2 \
	|| fail "LogDNS recorded more than one example.com request; DNS cache was bypassed"

send_dns_query_and_wait "DnsQuery I2" "200:6fc8:9220:f400:5cc2:305a:4ac6:967e"
send_dns_query_and_wait "DnsQuery ZG" "11.11.11.11"
send_dns_query_and_wait "DnsQuery HF" "10.10.10.10"
assert_log_dns_count_stays 1 2 \
	|| fail "LogDNS recorded another example.com request during DNS query checks"

send_http_client_and_wait
assert_curl_proxy_reaches_http_server

wait_log_contains "almagest/HttpServer" "GET /" \
	|| fail "HttpServer did not log the HTTP request"
wait_log_contains "almagest/LocalProxy" "connect example2.test:8080" \
	|| fail "LocalProxy did not log the proxied example2.test connect"

retry "$ALMAGEST_E2E_TIMEOUT" "system dig resolves through DefaultTun DNS" \
	system_dig_resolves_to example2.test 11.11.11.11 \
	|| fail "system dig did not resolve through the connected DefaultTun DNS path"
retry "$ALMAGEST_E2E_TIMEOUT" "IPv6 DNS packet resolves through Tun2Dns" \
	system_ipv6_dig_resolves_to example2.test 11.11.11.11 \
	|| fail "IPv6 DNS packet did not resolve through Tun2Dns"

delete_dns_input_link "DefaultTun BS" "MeshNames xP"
delete_dns_input_link "Tun2Dns LN" "MeshNames xP"
wait_node_label "$tun2dns" "waiting for DNS" || fail "Tun2Dns did not notice DNS disconnection"

retry "$ALMAGEST_E2E_TIMEOUT" "system dig example2.test fails after DNS disconnection" \
	system_dig_has_no_answer example2.test \
	|| fail "system dig example2.test unexpectedly resolved after DNS disconnection"
retry "$ALMAGEST_E2E_TIMEOUT" "system dig example.com fails after DNS disconnection" \
	system_dig_has_no_answer example.com \
	|| fail "system dig example.com unexpectedly resolved after DNS disconnection"

send_dns_query_and_wait "DnsQuery LN" "93.184.216.34"
send_dns_query_and_wait "DnsQuery I2" "200:6fc8:9220:f400:5cc2:305a:4ac6:967e"
send_dns_query_and_wait "DnsQuery ZG" "11.11.11.11"
send_dns_query_and_wait "DnsQuery HF" "10.10.10.10"
assert_log_dns_count_stays 1 2 \
	|| fail "internal cached DNS query reached LogDNS after DefaultTun/Tun2Dns DNS disconnection"

log "case passed"
