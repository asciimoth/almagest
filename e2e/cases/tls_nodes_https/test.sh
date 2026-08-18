#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

ALMAGEST_E2E_TIMEOUT="${ALMAGEST_E2E_TIMEOUT:-30}"

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

assert_socks_https_body() {
	local socks_port="$1"
	local ca_file="$2"
	local url="$3"
	local expected="$4"
	local out="$E2E_RUNTIME/$(echo "$url" | tr -c 'A-Za-z0-9' '_').txt"

	retry "$ALMAGEST_E2E_TIMEOUT" "curl HTTPS $url through SOCKS port $socks_port" \
		curl -g --fail --silent --show-error \
			--socks5-hostname "127.0.0.1:$socks_port" \
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

mitm_pid="$(start_static_https_target 127.0.0.1 18443 "mitm body" "$E2E_RUNTIME/mitm-target.log" "$E2E_CONFIG_DIR/origin.pem" "$E2E_CONFIG_DIR/origin.key")"
wrapper_pid="$(start_static_https_target 127.0.0.1 18444 "wrapper body" "$E2E_RUNTIME/wrapper-target.log" "$E2E_CONFIG_DIR/wrapper-origin.pem" "$E2E_CONFIG_DIR/wrapper-origin.key")"
terminator_pid="$(start_static_http_target 127.0.0.1 18080 "terminator body" "$E2E_RUNTIME/terminator-target.log")"

cleanup() {
	stop_static_http_target "$mitm_pid"
	stop_static_http_target "$wrapper_pid"
	stop_static_http_target "$terminator_pid"
	stop_daemon
}
trap cleanup EXIT

start_daemon

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

assert_socks_https_body 1181 "$E2E_RUNTIME/local-ca.pem" "https://origin.test:18443/mitm" "mitm body"
assert_socks_https_body 1182 "$E2E_RUNTIME/local-ca.pem" "https://terminator.test:18080/terminated" "terminator body"

cli graph click-menu-button --name "HttpClient Wrapper" --block request --button send >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "HttpClient wrapper response" http_client_ready \
	|| fail "HttpClient did not receive the wrapper target response"

retry "$ALMAGEST_E2E_TIMEOUT" "MITM HTTPS target access log" \
	grep -F "GET /mitm HTTP/1.1" "$E2E_RUNTIME/mitm-target.log" >/dev/null \
	|| fail "MITM target was not reached"
retry "$ALMAGEST_E2E_TIMEOUT" "terminator plaintext target access log" \
	grep -F "GET /terminated HTTP/1.1" "$E2E_RUNTIME/terminator-target.log" >/dev/null \
	|| fail "terminator plaintext target was not reached"
retry "$ALMAGEST_E2E_TIMEOUT" "wrapper HTTPS target access log" \
	grep -F "GET /wrapped HTTP/1.1" "$E2E_RUNTIME/wrapper-target.log" >/dev/null \
	|| fail "wrapper HTTPS target was not reached"

log "case passed"
