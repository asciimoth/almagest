#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

ALMAGEST_E2E_TIMEOUT="${ALMAGEST_E2E_TIMEOUT:-30}"
export ALMAGEST_E2E_TMP_FILE_STORAGE=0
export ALMAGEST_E2E_FILE_STORAGE=/var/lib/almagest-e2e-tls-nodes

start_static_https_target() {
	local host="$1"
	local port="$2"
	local body="$3"
	local access_log="$4"
	local cert="$5"
	local key="$6"

	rm -f "$access_log"
	python3 -u - "$host" "$port" "$body" "$access_log" "$cert" "$key" <<'PY' >/dev/null 2>"$access_log.stderr" &
import http.server
import ssl
import sys

host = sys.argv[1]
port = int(sys.argv[2])
body = sys.argv[3].encode()
access_log = sys.argv[4]
cert = sys.argv[5]
key = sys.argv[6]


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        alpn = ""
        if hasattr(self.request, "selected_alpn_protocol"):
            alpn = self.request.selected_alpn_protocol() or ""
        with open(access_log, "a", encoding="utf-8") as out:
            out.write(f"{self.command} {self.path} {self.request_version} host={self.headers.get('Host', '')} alpn={alpn}\n")
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        return


server = http.server.ThreadingHTTPServer((host, port), Handler)
ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain(cert, key)
ctx.set_alpn_protocols(["http/1.1"])
server.socket = ctx.wrap_socket(server.socket, server_side=True)
server.serve_forever()
PY
	echo "$!"
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

http_client_ready() {
	local status
	local err
	local resp
	status="$(menu_text "HttpClient Wrapper" result status)" || return 1
	err="$(menu_text "HttpClient Wrapper" result error)" || return 1
	resp="$(menu_text "HttpClient Wrapper" result response)" || return 1
	[[ "$status" == "Status: 200 OK" && -z "$err" && "$resp" == "wrapper body" ]]
}

http_client_failed() {
	[[ "$(menu_text "HttpClient Wrapper" result status)" == "Status: fail" \
		&& -n "$(menu_text "HttpClient Wrapper" result error)" ]]
}

send_http_client_expect_success() {
	cli graph click-menu-button --name "HttpClient Wrapper" --block request --button send >/dev/null
	retry "$ALMAGEST_E2E_TIMEOUT" "HttpClient wrapper response" http_client_ready \
		|| fail "HttpClient did not receive the wrapper target response"
}

send_http_client_expect_failure() {
	cli graph click-menu-button --name "HttpClient Wrapper" --block request --button send >/dev/null
	retry "$ALMAGEST_E2E_TIMEOUT" "HttpClient wrapper failure" http_client_failed \
		|| fail "HttpClient did not report a wrapper request failure"
}

assert_socks_https_body() {
	local socks_port="$1"
	local ca_file="$2"
	local url="$3"
	local expected="$4"
	local out
	local authority="${url#https://}"
	authority="${authority%%/*}"
	out="$E2E_RUNTIME/$(echo "$url" | tr -c 'A-Za-z0-9' '_').txt"

	retry "$ALMAGEST_E2E_TIMEOUT" "curl HTTPS $url through SOCKS port $socks_port" \
		curl -g --fail --silent --show-error \
			--socks5 "127.0.0.1:$socks_port" \
			--resolve "$authority:127.0.0.1" \
			--http1.1 \
			--cacert "$ca_file" \
			"$url" \
			--output "$out" \
		|| fail "curl HTTPS request failed: $url"

	local body
	body="$(cat "$out")"
	[[ "$body" == "$expected" ]] \
		|| fail "unexpected body for $url: $body"
}

socks_https_fails() {
	local socks_port="$1"
	local ca_file="$2"
	local url="$3"
	local authority="${url#https://}"
	authority="${authority%%/*}"

	! curl -g --fail --silent --show-error --max-time 2 \
		--socks5 "127.0.0.1:$socks_port" \
		--resolve "$authority:127.0.0.1" \
		--http1.1 \
		--cacert "$ca_file" \
		"$url" \
		--output /dev/null \
		2>/dev/null
}

assert_socks_https_failure() {
	local socks_port="$1"
	local ca_file="$2"
	local url="$3"

	retry "$ALMAGEST_E2E_TIMEOUT" "curl HTTPS $url fails through SOCKS port $socks_port" \
		socks_https_fails "$socks_port" "$ca_file" "$url" \
		|| fail "curl HTTPS request stayed usable: $url"
}

apply_mitm_config() {
	local tls_config="$1"
	local sniff_buffer="$2"
	local leaf_ttl="$3"
	local filter_mode="$4"
	local filter_rules="$5"

	jq -n \
		--arg tlsConfig "$tls_config" \
		--argjson sniffBufferSize "$sniff_buffer" \
		--argjson leafTTLSeconds "$leaf_ttl" \
		--arg filterMode "$filter_mode" \
		--arg filterRules "$filter_rules" \
		'{config: {
			tlsConfig: $tlsConfig,
			sniffBufferSize: $sniffBufferSize,
			leafTTLSeconds: $leafTTLSeconds,
			filterMode: $filterMode,
			filterRules: $filterRules
		}}' | cli graph apply-menu --name "TlsMitm 3102" >/dev/null
}

apply_terminator_config() {
	local sniff_buffer="$1"
	local leaf_ttl="$2"
	local use_sni="$3"
	local server_alpn="$4"
	local remaps="$5"

	jq -n \
		--argjson sniffBufferSize "$sniff_buffer" \
		--argjson leafTTLSeconds "$leaf_ttl" \
		--argjson useSNIHostname "$use_sni" \
		--arg serverAlpn "$server_alpn" \
		--arg destinationRemaps "$remaps" \
		'{config: {
			sniffBufferSize: $sniffBufferSize,
			leafTTLSeconds: $leafTTLSeconds,
			useSNIHostname: $useSNIHostname,
			serverAlpn: $serverAlpn,
			destinationRemaps: $destinationRemaps
		}}' | cli graph apply-menu --name "TlsTerminator 3202" >/dev/null
}

apply_wrapper_config() {
	local tls_config="$1"
	local client_server_name="$2"
	local client_alpn="$3"
	local server_name="$4"
	local server_alpn="$5"
	local leaf_ttl="$6"

	jq -n \
		--arg tlsConfig "$tls_config" \
		--arg clientServerName "$client_server_name" \
		--arg clientAlpn "$client_alpn" \
		--arg serverName "$server_name" \
		--arg serverAlpn "$server_alpn" \
		--argjson leafTTLSeconds "$leaf_ttl" \
		'{config: {
			tlsConfig: $tlsConfig,
			clientServerName: $clientServerName,
			clientAlpn: $clientAlpn,
			serverName: $serverName,
			serverAlpn: $serverAlpn,
			leafTTLSeconds: $leafTTLSeconds
		}}' | cli graph apply-menu --name "TlsWrapper 3302" >/dev/null
}

assert_all_tls_paths() {
	local ca_file="$1"

	assert_socks_https_body 1181 "$ca_file" "https://origin.test:18443/mitm" "mitm body"
	assert_socks_https_body 1182 "$ca_file" "https://terminator.test:18080/terminated" "terminator body"
	send_http_client_expect_success
}

mitm_pid=""
wrapper_pid=""
terminator_pid=""

cleanup() {
	trap - EXIT
	stop_daemon
	stop_static_http_target "$mitm_pid"
	stop_static_http_target "$wrapper_pid"
	stop_static_http_target "$terminator_pid"
}
trap cleanup EXIT

mitm_pid="$(start_static_https_target 127.0.0.1 18443 "mitm body" "$E2E_RUNTIME/mitm-target.log" "$E2E_CONFIG_DIR/origin.pem" "$E2E_CONFIG_DIR/origin.key")"
wrapper_pid="$(start_static_https_target 127.0.0.1 18444 "wrapper body" "$E2E_RUNTIME/wrapper-target.log" "$E2E_CONFIG_DIR/wrapper-origin.pem" "$E2E_CONFIG_DIR/wrapper-origin.key")"
terminator_pid="$(start_static_http_target 127.0.0.1 18080 "terminator body" "$E2E_RUNTIME/terminator-target.log")"
start_daemon
trap cleanup EXIT

wait_node_class_available "almagest/TlsMitm" || fail "TlsMitm class missing"
wait_node_class_available "almagest/TlsTerminator" || fail "TlsTerminator class missing"
wait_node_class_available "almagest/TlsWrapper" || fail "TlsWrapper class missing"

mitm_id="$(node_id_by_name "TlsMitm 3102")"
terminator_id="$(node_id_by_name "TlsTerminator 3202")"
wrapper_id="$(node_id_by_name "TlsWrapper 3302")"
wait_node_label "$mitm_id" "intercepting TLS" || fail "TlsMitm did not become ready"
wait_node_label "$terminator_id" "terminating TLS" || fail "TlsTerminator did not become ready"
wait_node_label "$wrapper_id" "wrapping TLS" || fail "TlsWrapper did not become ready"
wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:1181" \
	|| fail "MITM local proxy did not listen"
wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:1182" \
	|| fail "terminator local proxy did not listen"

cli tls builtin-ca | jq -er '.cert.pem' >"$E2E_RUNTIME/local-ca.pem"

assert_all_tls_paths "$E2E_RUNTIME/local-ca.pem"

retry "$ALMAGEST_E2E_TIMEOUT" "MITM HTTPS target access log" \
	grep -F "GET /mitm HTTP/1.1" "$E2E_RUNTIME/mitm-target.log" >/dev/null \
	|| fail "MITM target was not reached"
retry "$ALMAGEST_E2E_TIMEOUT" "terminator plaintext target access log" \
	grep -F "GET /terminated HTTP/1.1" "$E2E_RUNTIME/terminator-target.log" >/dev/null \
	|| fail "terminator plaintext target was not reached"
retry "$ALMAGEST_E2E_TIMEOUT" "wrapper HTTPS target access log" \
	grep -F "GET /wrapped HTTP/1.1" "$E2E_RUNTIME/wrapper-target.log" >/dev/null \
	|| fail "wrapper HTTPS target was not reached"

# Switch both upstream-verifying nodes to one mutable TLS policy. The live
# forms also exercise non-default interception, remap, ALPN, server-name, and
# certificate-lifetime settings while all three data paths are active.
cli tls set-cert --name wrong-ca --pem-file "$E2E_CONFIG_DIR/wrong-ca.pem" >/dev/null
cli tls set-policy --name live-origin-policy \
	--spec '{"trust_anchors":[{"cert":"origin-ca","scope":{"any":true}}]}' >/dev/null

mitm_rules='[{"sni_hosts":["origin.test"]}]'
terminator_remaps='[{"sni_hosts":["terminator.test"],"dst":"127.0.0.1:18080"}]'
apply_mitm_config live-origin-policy 4096 120 inclusive "$mitm_rules"
apply_terminator_config 4096 120 true http/1.1 "$terminator_remaps"
apply_wrapper_config live-origin-policy wrapper-origin.test http/1.1 wrapper.test http/1.1 120
wait_node_label "$mitm_id" "intercepting TLS" || fail "valid MITM form was not applied"
wait_node_label "$terminator_id" "terminating TLS" || fail "valid terminator form was not applied"
wait_node_label "$wrapper_id" "wrapping TLS" || fail "valid wrapper form was not applied"
assert_all_tls_paths "$E2E_RUNTIME/local-ca.pem"

# Each invalid form closes only its own emitted wrapper. A complete valid form
# must rebuild the wrapper and make the same real traffic work again.
apply_mitm_config live-origin-policy 4096 120 inclusive '{'
wait_node_label "$mitm_id" "invalid config" || fail "MITM accepted invalid filter rules"
assert_socks_https_failure 1181 "$E2E_RUNTIME/local-ca.pem" "https://origin.test:18443/mitm"
assert_socks_https_body 1182 "$E2E_RUNTIME/local-ca.pem" "https://terminator.test:18080/terminated" "terminator body"
apply_mitm_config live-origin-policy 4096 120 inclusive "$mitm_rules"
wait_node_label "$mitm_id" "intercepting TLS" || fail "MITM did not recover after a valid form"
assert_socks_https_body 1181 "$E2E_RUNTIME/local-ca.pem" "https://origin.test:18443/mitm" "mitm body"

apply_wrapper_config live-origin-policy wrapper-origin.test http/1.1 "" http/1.1 120
wait_node_label "$wrapper_id" "invalid config" || fail "wrapper accepted an empty server name"
send_http_client_expect_failure
apply_wrapper_config live-origin-policy wrapper-origin.test http/1.1 wrapper.test http/1.1 120
wait_node_label "$wrapper_id" "wrapping TLS" || fail "wrapper did not recover after a valid form"
send_http_client_expect_success

apply_terminator_config 4096 120 true http/1.1 '{'
wait_node_label "$terminator_id" "invalid config" || fail "terminator accepted invalid remaps"
assert_socks_https_failure 1182 "$E2E_RUNTIME/local-ca.pem" "https://terminator.test:18080/terminated"
assert_socks_https_body 1181 "$E2E_RUNTIME/local-ca.pem" "https://origin.test:18443/mitm" "mitm body"
apply_terminator_config 4096 120 true http/1.1 "$terminator_remaps"
wait_node_label "$terminator_id" "terminating TLS" || fail "terminator did not recover after a valid form"
assert_socks_https_body 1182 "$E2E_RUNTIME/local-ca.pem" "https://terminator.test:18080/terminated" "terminator body"

# A selected policy update must replace the MITM and wrapper outputs. The
# wrong CA is structurally valid, so failure is observable only in real TLS
# handshakes. Restoring the policy must rebuild both paths again.
cli tls set-policy --name live-origin-policy \
	--spec '{"trust_anchors":[{"cert":"wrong-ca","scope":{"any":true}}]}' >/dev/null
assert_socks_https_failure 1181 "$E2E_RUNTIME/local-ca.pem" "https://origin.test:18443/mitm"
send_http_client_expect_failure
assert_socks_https_body 1182 "$E2E_RUNTIME/local-ca.pem" "https://terminator.test:18080/terminated" "terminator body"

cli tls set-policy --name live-origin-policy \
	--spec '{"trust_anchors":[{"cert":"origin-ca","scope":{"any":true}}]}' >/dev/null
assert_all_tls_paths "$E2E_RUNTIME/local-ca.pem"

# Rotation broadcasts a local-CA notification to every TLS node. Existing
# client trust must stop working for the MITM and terminator branches. The new
# built-in CA must then validate freshly generated client-facing certificates.
cp "$E2E_RUNTIME/local-ca.pem" "$E2E_RUNTIME/old-local-ca.pem"
cli tls set-builtin-ca \
	--issuer "E2E rotated local CA" \
	--validity-days 30 \
	--key-algorithm ed25519 >/dev/null
assert_socks_https_failure 1181 "$E2E_RUNTIME/old-local-ca.pem" "https://origin.test:18443/mitm"
assert_socks_https_failure 1182 "$E2E_RUNTIME/old-local-ca.pem" "https://terminator.test:18080/terminated"
cli tls builtin-ca | jq -er '.cert.pem' >"$E2E_RUNTIME/local-ca.pem"
assert_all_tls_paths "$E2E_RUNTIME/local-ca.pem"

# Remove and restore each provider link. The affected node must leave its
# ready state, close the consumer wrapper, and build a new wrapper after the
# same provider is attached again.
out_mitm_id="$(node_id_by_name "OutNetwork MITM")"
out_terminator_id="$(node_id_by_name "OutNetwork Terminator")"
out_wrapper_id="$(node_id_by_name "OutNetwork Wrapper")"

mitm_input="$(link_id_by_port_names "TlsMitm 3102" Network "OutNetwork MITM" Network)"
cli graph delete-link --id "$mitm_input" >/dev/null
wait_node_label "$mitm_id" "waiting for network" || fail "MITM did not notice its removed input"
assert_socks_https_failure 1181 "$E2E_RUNTIME/local-ca.pem" "https://origin.test:18443/mitm"
add_link_by_port_name "$mitm_id" right Network "$out_mitm_id" left Network >/dev/null
wait_node_label "$mitm_id" "intercepting TLS" || fail "MITM did not recover its input"
assert_socks_https_body 1181 "$E2E_RUNTIME/local-ca.pem" "https://origin.test:18443/mitm" "mitm body"

terminator_input="$(link_id_by_port_names "TlsTerminator 3202" Network "OutNetwork Terminator" Network)"
cli graph delete-link --id "$terminator_input" >/dev/null
wait_node_label "$terminator_id" "waiting for network" || fail "terminator did not notice its removed input"
assert_socks_https_failure 1182 "$E2E_RUNTIME/local-ca.pem" "https://terminator.test:18080/terminated"
add_link_by_port_name "$terminator_id" right Network "$out_terminator_id" left Network >/dev/null
wait_node_label "$terminator_id" "terminating TLS" || fail "terminator did not recover its input"
assert_socks_https_body 1182 "$E2E_RUNTIME/local-ca.pem" "https://terminator.test:18080/terminated" "terminator body"

wrapper_input="$(link_id_by_port_names "TlsWrapper 3302" Network "OutNetwork Wrapper" Network)"
cli graph delete-link --id "$wrapper_input" >/dev/null
wait_node_label "$wrapper_id" "waiting for network" || fail "wrapper did not notice its removed input"
send_http_client_expect_failure
add_link_by_port_name "$wrapper_id" right Network "$out_wrapper_id" left Network >/dev/null
wait_node_label "$wrapper_id" "wrapping TLS" || fail "wrapper did not recover its input"
send_http_client_expect_success

# The daemon save path must retain the final live forms, mutable TLS objects,
# rotated CA, and graph links. The external targets stay up during restart.
stop_daemon
start_daemon
mitm_id="$(node_id_by_name "TlsMitm 3102")"
terminator_id="$(node_id_by_name "TlsTerminator 3202")"
wrapper_id="$(node_id_by_name "TlsWrapper 3302")"
wait_node_label "$mitm_id" "intercepting TLS" || fail "MITM did not recover after restart"
wait_node_label "$terminator_id" "terminating TLS" || fail "terminator did not recover after restart"
wait_node_label "$wrapper_id" "wrapping TLS" || fail "wrapper did not recover after restart"
cli tls list | jq -e '
	any(.policies[]; .name == "live-origin-policy")
	and any(.certs[]; .name == "wrong-ca")
' >/dev/null || fail "mutable TLS objects did not survive restart"
cli tls builtin-ca | jq -er '.cert.pem' >"$E2E_RUNTIME/local-ca.pem"
assert_all_tls_paths "$E2E_RUNTIME/local-ca.pem"

log "case passed"
