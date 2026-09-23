#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

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
	local node="$1"
	local answer="$2"
	[[ "$(menu_text "$node" result status)" == "Status: NOERROR" \
		&& -z "$(menu_text "$node" result error)" \
		&& "$(menu_text "$node" result response)" == *"$answer"* ]]
}

dns_query_is_unsuccessful() {
	local node="$1"
	local status
	status="$(menu_text "$node" result status)"
	[[ "$status" == "Status: fail" || "$status" == "Status: SERVFAIL" ]]
}

send_query_expect_answer_once() {
	local node="$1"
	local answer="$2"
	local description="$3"
	cli graph click-menu-button --name "$node" --block request --button send >/dev/null
	retry "$ALMAGEST_E2E_TIMEOUT" "$description" \
		dns_query_has_answer "$node" "$answer" \
		|| fail "$description did not return $answer"
}

send_query_expect_error_once() {
	local node="$1"
	local description="$2"
	cli graph click-menu-button --name "$node" --block request --button send >/dev/null
	retry "$ALMAGEST_E2E_TIMEOUT" "$description" \
		dns_query_is_unsuccessful "$node" \
		|| fail "$description did not report an unsuccessful DNS result"
}

http_client_has_body() {
	[[ "$(menu_text "HTTP Client" result status)" == "Status: 200 OK" \
		&& -z "$(menu_text "HTTP Client" result error)" \
		&& "$(menu_text "HTTP Client" result response)" == "dns-transport-stack-ok" ]]
}

http_client_has_error() {
	[[ "$(menu_text "HTTP Client" result status)" == "Status: fail" \
		&& -n "$(menu_text "HTTP Client" result error)" ]]
}

send_http_expect_success_once() {
	local description="$1"
	cli graph click-menu-button --name "HTTP Client" --block request --button send >/dev/null
	retry "$ALMAGEST_E2E_TIMEOUT" "$description" http_client_has_body \
		|| fail "$description did not return the expected HTTP body"
}

send_http_expect_error_once() {
	local description="$1"
	cli graph click-menu-button --name "HTTP Client" --block request --button send >/dev/null
	retry "$ALMAGEST_E2E_TIMEOUT" "$description" http_client_has_error \
		|| fail "$description did not report an HTTP error"
}

socks_body_is_expected() {
	local output=/tmp/dns-transport-stack-socks.body
	curl --fail --silent --show-error --max-time 2 \
		--socks5-hostname 127.0.0.1:1184 \
		http://web.stack.test:8080/ \
		--output "$output" \
		&& [[ "$(<"$output")" == "dns-transport-stack-ok" ]]
}

socks_request_fails() {
	! curl --fail --silent --show-error --max-time 1 \
		--socks5-hostname 127.0.0.1:1184 \
		http://web.stack.test:8080/ \
		--output /dev/null \
		2>/dev/null
}

assert_socks_success() {
	local description="$1"
	retry "$ALMAGEST_E2E_TIMEOUT" "$description" socks_body_is_expected \
		|| fail "$description did not return the expected body"
}

assert_socks_failure() {
	local description="$1"
	retry "$ALMAGEST_E2E_TIMEOUT" "$description" socks_request_fails \
		|| fail "$description unexpectedly succeeded"
}

log_dns_request_count() {
	local name="$1"
	cli log subscribe | jq -er --arg question "questions=$name A" '
		[
			.entries[]
			| select(
				.node_class == "almagest/LogDNS"
				and (.message | contains("dns.Request "))
				and (.message | contains($question))
			)
		]
		| length
	'
}

log_dns_request_count_is() {
	local name="$1"
	local expected="$2"
	[[ "$(log_dns_request_count "$name")" == "$expected" ]]
}

apply_dns_server() {
	local network="$1"
	local port="$2"
	jq -nc \
		--arg network "$network" \
		--argjson port "$port" \
		'{listen:{network:$network,host:"127.0.0.1",port:$port,requestTimeoutMS:500}}' \
		| cli graph apply-menu --name "DNS Server" >/dev/null
}

apply_dns_client() {
	local server="$1"
	jq -nc --arg server "$server" \
		'{config:{servers:$server,requestTimeoutMS:500}}' \
		| cli graph apply-menu --name "DNS Client" >/dev/null
}

start_daemon

for node in \
	"DNS Transport Network" \
	"Application Network" \
	"Primary DNS" \
	"Secondary DNS" \
	"DNS Log" \
	"DNS Server" \
	"DNS Client" \
	"DNS Cache" \
	"DNS Inject" \
	"DNS From Injected Network" \
	"Cache Query" \
	"Server Gap Query" \
	"Secondary Query" \
	"Client Gap Query" \
	"Invalid Config Query" \
	"Port Change Query" \
	"Restart Query" \
	"Adapter Query" \
	"Adapter After Switch Query" \
	"HTTP Server" \
	"HTTP Client" \
	"Local Proxy"; do
	wait_graph_node "$node" || fail "$node node missing"
done

transport_network="$(node_id_by_name "DNS Transport Network")"
application_network="$(node_id_by_name "Application Network")"
secondary="$(node_id_by_name "Secondary DNS")"
dns_log="$(node_id_by_name "DNS Log")"
dns_server="$(node_id_by_name "DNS Server")"
dns_client="$(node_id_by_name "DNS Client")"
dns_cache="$(node_id_by_name "DNS Cache")"
dns_inject="$(node_id_by_name "DNS Inject")"
dns_adapter="$(node_id_by_name "DNS From Injected Network")"
local_proxy="$(node_id_by_name "Local Proxy")"

wait_node_label "$dns_server" "listening 127.0.0.1:5353" \
	|| fail "DNS Server did not become ready"
wait_node_label "$dns_client" "1 DNS server" \
	|| fail "DNS Client did not become ready"
wait_node_label "$dns_cache" "caching DNS" \
	|| fail "DNS Cache did not become ready"
wait_node_label "$dns_inject" "injecting DNS" \
	|| fail "DNS Inject did not become ready"
wait_node_label "$dns_adapter" "resolving DNS" \
	|| fail "Dns2Network did not become ready"
wait_log_contains "almagest/HttpServer" "listening on 127.0.0.1:8080" \
	|| fail "HTTP Server did not listen"
wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:1184" \
	|| fail "Local Proxy did not listen"

# Send one request after the graph advertises readiness. Retrying only observes
# that request's result; it does not issue a replacement request.
send_query_expect_answer_once "Cache Query" "192.0.2.10" \
	"first DNS request through UDP transport"
send_query_expect_answer_once "Adapter Query" "198.51.100.10" \
	"first request through DnsInject and Dns2Network"
send_http_expect_success_once "first HTTP request through injected DNS"
assert_socks_success "first SOCKS request through injected DNS"

# The second cache query must not reach the server-side logging wrapper.
retry "$ALMAGEST_E2E_TIMEOUT" "one upstream cached.stack.test request" \
	log_dns_request_count_is "cached.stack.test." 1 \
	|| fail "the initial cached.stack.test request did not reach LogDNS once"
send_query_expect_answer_once "Cache Query" "192.0.2.10" \
	"repeated cached DNS request"
[[ "$(log_dns_request_count "cached.stack.test.")" == 1 ]] \
	|| fail "DnsCache sent a repeated cached.stack.test request upstream"

# Detaching only the server upstream must not close its UDP listener. Cached
# data remains usable, while a new name fails until the same link is restored.
server_dns_link="$(link_id_by_port_names "DNS Server" DNS "DNS Log" DNS)"
cli graph delete-link --id "$server_dns_link" >/dev/null
wait_node_label "$dns_server" "waiting for DNS" \
	|| fail "DNS Server did not notice its removed upstream"
send_query_expect_answer_once "Cache Query" "192.0.2.10" \
	"cached request while DNS Server has no upstream"
send_query_expect_error_once "Server Gap Query" \
	"uncached request while DNS Server has no upstream"
add_link_by_port_name "$dns_server" right DNS "$dns_log" left DNS >/dev/null
wait_node_label "$dns_server" "listening 127.0.0.1:5353" \
	|| fail "DNS Server did not accept its restored upstream"
send_query_expect_answer_once "Server Gap Query" "192.0.2.11" \
	"request after DNS Server upstream restore"

# Replace the source below LogDNS. This propagates new wrappers through every
# layer without restarting the UDP listener.
log_input="$(link_id_by_port_names "DNS Log" DNS "Primary DNS" DNS)"
cli graph delete-link --id "$log_input" >/dev/null
send_query_expect_error_once "Secondary Query" \
	"request while LogDNS has no source"
add_link_by_port_name "$dns_log" right DNS "$secondary" left DNS >/dev/null
send_query_expect_answer_once "Secondary Query" "192.0.2.21" \
	"request after replacing the static DNS source"
send_query_expect_answer_once "Adapter After Switch Query" "198.51.100.21" \
	"Dns2Network request after replacing the DNS source"

# Invalid forms report their errors but must not destroy the last valid runtime
# path. Applying the valid values again clears the error and rebuilds only when
# required.
apply_dns_server tcp 5353
wait_node_label "$dns_server" "invalid config" \
	|| fail "DNS Server did not reject an unsupported network"
send_query_expect_answer_once "Invalid Config Query" "192.0.2.23" \
	"request through DNS Server after invalid edit"
apply_dns_server udp 5353
wait_node_label "$dns_server" "listening 127.0.0.1:5353" \
	|| fail "DNS Server did not recover from its invalid edit"

apply_dns_client "bad://server"
wait_node_label "$dns_client" "invalid config" \
	|| fail "DNS Client did not reject an invalid server URL"
send_http_expect_success_once "HTTP request through DNS Client after invalid edit"
apply_dns_client "udp://127.0.0.1:5353"
wait_node_label "$dns_client" "1 DNS server" \
	|| fail "DNS Client did not recover from its invalid edit"

# A removed transport input invalidates emitted client wrappers. Cached data is
# still available, but a new lookup fails until a new network wrapper arrives.
client_network_link="$(link_id_by_port_names "DNS Client" Network "DNS Transport Network" Network)"
cli graph delete-link --id "$client_network_link" >/dev/null
wait_node_label "$dns_client" "waiting for network" \
	|| fail "DNS Client did not notice its removed network"
wait_node_label "$dns_cache" "cache-only DNS" \
	|| fail "DNS Cache did not notice that its linked DNS Client became unavailable"
send_query_expect_answer_once "Cache Query" "192.0.2.10" \
	"cached request while DNS Client is detached"
send_query_expect_error_once "Client Gap Query" \
	"uncached request while DNS Client is detached"
cache_gap_started="$SECONDS"
send_query_expect_error_once "Restart Query" \
	"uncached request through DNS Cache while DNS Client is detached"
cache_gap_elapsed=$((SECONDS - cache_gap_started))
((cache_gap_elapsed <= 3)) \
	|| fail "DNS Cache miss took ${cache_gap_elapsed}s after its linked DNS Client became unavailable"
add_link_by_port_name "$dns_client" right Network "$transport_network" left Network >/dev/null
wait_node_label "$dns_client" "1 DNS server" \
	|| fail "DNS Client did not accept its replacement network"
wait_node_label "$dns_cache" "caching DNS" \
	|| fail "DNS Cache did not accept the replacement DNS Client output"
send_query_expect_answer_once "Client Gap Query" "192.0.2.22" \
	"request after DNS Client network restore"

# Removing injector inputs must close both its direct output and the wrapper
# used by Dns2Network. Each restored input must publish a usable replacement.
inject_dns_link="$(link_id_by_port_names "DNS Inject" DNS "DNS Cache" DNS)"
cli graph delete-link --id "$inject_dns_link" >/dev/null
wait_node_label "$dns_inject" "waiting for DNS" \
	|| fail "DNS Inject did not notice its removed DNS input"
send_http_expect_error_once "HTTP request without injected DNS"
send_query_expect_error_once "Adapter After Switch Query" \
	"Dns2Network request without injected DNS"
add_link_by_port_name "$dns_inject" right DNS "$dns_cache" left DNS >/dev/null
wait_node_label "$dns_inject" "injecting DNS" \
	|| fail "DNS Inject did not accept its restored DNS input"
send_http_expect_success_once "first HTTP request after DNS input restore"
send_query_expect_answer_once "Adapter After Switch Query" "198.51.100.21" \
	"first Dns2Network request after DNS input restore"

inject_network_link="$(link_id_by_port_names "DNS Inject" Network "Application Network" Network)"
cli graph delete-link --id "$inject_network_link" >/dev/null
wait_node_label "$dns_inject" "waiting for network" \
	|| fail "DNS Inject did not notice its removed network input"
send_http_expect_error_once "HTTP request without injector network"
add_link_by_port_name "$dns_inject" right Network "$application_network" left Network >/dev/null
wait_node_label "$dns_inject" "injecting DNS" \
	|| fail "DNS Inject did not accept its restored network input"
send_http_expect_success_once "first HTTP request after injector network restore"

# One output link owns one wrapper. Removing LocalProxy must not affect the
# independent HTTP Client or Dns2Network consumers.
proxy_link="$(link_id_by_port_names "Local Proxy" proxy "DNS Inject" Network)"
cli graph delete-link --id "$proxy_link" >/dev/null
assert_socks_failure "SOCKS request with Local Proxy detached"
send_http_expect_success_once "HTTP request with independent proxy detached"
send_query_expect_answer_once "Adapter After Switch Query" "198.51.100.21" \
	"Dns2Network request with independent proxy detached"
add_link_by_port_name "$local_proxy" right proxy "$dns_inject" left Network >/dev/null
assert_socks_success "first SOCKS request after output relink"

# Move the UDP listener before the client. The old destination must fail for a
# new name, then the current destination must recover without graph rebuild.
apply_dns_server udp 5354
wait_node_label "$dns_server" "listening 127.0.0.1:5354" \
	|| fail "DNS Server did not move to port 5354"
send_query_expect_error_once "Port Change Query" \
	"request through stale DNS Client destination"
apply_dns_client "udp://127.0.0.1:5354"
wait_node_label "$dns_client" "1 DNS server" \
	|| fail "DNS Client did not accept port 5354"
send_query_expect_answer_once "Port Change Query" "192.0.2.24" \
	"request after DNS client destination update"
send_http_expect_success_once "HTTP request after DNS transport port migration"

# Form and graph edits are saved. A fresh process must reconstruct the complete
# chain and make the first request usable at the new UDP destination.
stop_daemon
start_daemon
dns_server="$(node_id_by_name "DNS Server")"
dns_client="$(node_id_by_name "DNS Client")"
dns_inject="$(node_id_by_name "DNS Inject")"
wait_node_label "$dns_server" "listening 127.0.0.1:5354" \
	|| fail "restart did not restore DNS Server port 5354"
wait_node_label "$dns_client" "1 DNS server" \
	|| fail "restart did not restore DNS Client"
wait_node_label "$dns_inject" "injecting DNS" \
	|| fail "restart did not restore DNS Inject"
send_query_expect_answer_once "Restart Query" "192.0.2.25" \
	"first DNS request after daemon restart"
send_http_expect_success_once "first HTTP request after daemon restart"
assert_socks_success "first SOCKS request after daemon restart"

log "case passed"
