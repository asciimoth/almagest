#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

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
            out.write(f"{self.command} {self.path} {self.request_version} alpn={alpn}\n")
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

assert_socks_body() {
	local url="$1"
	local expected="$2"
	local out
	out="$E2E_RUNTIME/$(echo "$url" | tr -c 'A-Za-z0-9' '_').txt"

	retry "$ALMAGEST_E2E_TIMEOUT" "curl $url through sniffer" \
		curl -g --fail --silent --show-error \
			--socks5-hostname 127.0.0.1:1081 \
			"$url" \
			--output "$out" \
		|| fail "curl request failed: $url"

	local body
	body="$(cat "$out")"
	[[ "$body" == "$expected" ]] \
		|| fail "unexpected body for $url: $body"
}

assert_socks_https_body() {
	local url="$1"
	local expected="$2"
	local out="$E2E_RUNTIME/https.txt"

	retry "$ALMAGEST_E2E_TIMEOUT" "curl HTTPS $url through sniffer" \
		curl -g --fail --silent --show-error \
			--socks5-hostname 127.0.0.1:1081 \
			--http1.1 \
			--insecure \
			"$url" \
			--output "$out" \
		|| fail "curl HTTPS request failed: $url"

	local body
	body="$(cat "$out")"
	[[ "$body" == "$expected" ]] \
		|| fail "unexpected HTTPS body: $body"
}

assert_curl_fails() {
	local url="$1"
	if curl -g --fail --silent --show-error --max-time 3 \
		--socks5-hostname 127.0.0.1:1081 \
		"$url" \
		--output "$E2E_RUNTIME/unexpected.txt" \
		2>"$E2E_RUNTIME/expected-curl-failure.txt"; then
		fail "curl unexpectedly succeeded: $url"
	fi
}

http_alpha_pid="$(start_static_http_target 127.0.0.1 18081 "alpha body" "$E2E_RUNTIME/http-alpha.log")"
http_beta_pid="$(start_static_http_target 127.0.0.1 18082 "beta body" "$E2E_RUNTIME/http-beta.log")"
https_pid="$(start_static_https_target 127.0.0.1 18443 "tls body" "$E2E_RUNTIME/https.log" "$E2E_CONFIG_DIR/tls.crt" "$E2E_CONFIG_DIR/tls.key")"

cleanup() {
	stop_static_http_target "$http_alpha_pid"
	stop_static_http_target "$http_beta_pid"
	stop_static_http_target "$https_pid"
	stop_daemon
}

trap cleanup EXIT

start_daemon
trap cleanup EXIT

wait_graph_node "LocalProxy 2001" || fail "LocalProxy node missing"
wait_graph_node "Sniffer 2002" || fail "Sniffer node missing"
wait_graph_node "OutNetwork 2003" || fail "OutNetwork node missing"

sniffer_id="$(node_id_by_name "Sniffer 2002")"
wait_node_label "$sniffer_id" "sniffing 3 classifiers" \
	|| fail "Sniffer did not become ready"
wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:1081" \
	|| fail "local proxy did not start listening"

assert_socks_body "http://service.test/http-alpha" "alpha body"
assert_socks_body "http://service.test/http-beta" "beta body"
assert_curl_fails "http://service.test/rejected"
assert_socks_https_body "https://tls-service.test/secure" "tls body"

retry "$ALMAGEST_E2E_TIMEOUT" "HTTP alpha access log" \
	grep -F "GET /http-alpha HTTP/1.1" "$E2E_RUNTIME/http-alpha.log" >/dev/null \
	|| fail "HTTP alpha target was not reached"
retry "$ALMAGEST_E2E_TIMEOUT" "HTTP beta access log" \
	grep -F "GET /http-beta HTTP/1.1" "$E2E_RUNTIME/http-beta.log" >/dev/null \
	|| fail "HTTP beta target was not reached"
retry "$ALMAGEST_E2E_TIMEOUT" "HTTPS ALPN access log" \
	grep -F "GET /secure HTTP/1.1 alpn=http/1.1" "$E2E_RUNTIME/https.log" >/dev/null \
	|| fail "HTTPS target did not record expected ALPN"

log "case passed"
