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

start_static_ssh_target() {
	local host="$1"
	local port="$2"
	local access_log="$3"

	rm -f "$access_log"
	python3 -u - "$host" "$port" "$access_log" <<'PY' >"$access_log.stderr" 2>&1 &
import socket
import sys

host = sys.argv[1]
port = int(sys.argv[2])
access_log = sys.argv[3]

with socket.socket() as server:
    server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    server.bind((host, port))
    server.listen()
    while True:
        conn, _ = server.accept()
        with conn:
            payload = b""
            while not payload.endswith(b"\n") and len(payload) < 1024:
                chunk = conn.recv(1024 - len(payload))
                if not chunk:
                    break
                payload += chunk
            with open(access_log, "ab") as out:
                out.write(payload)
            if payload.startswith(b"SSH-"):
                conn.sendall(b"SSH-2.0-almagest-e2e-target\r\n")
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

assert_socks_ssh() {
	python3 - <<'PY'
import socket


def recv_exact(conn, size):
    data = b""
    while len(data) < size:
        chunk = conn.recv(size - len(data))
        if not chunk:
            raise RuntimeError("unexpected end of SOCKS response")
        data += chunk
    return data


with socket.create_connection(("127.0.0.1", 1081), timeout=3) as conn:
    conn.sendall(b"\x05\x01\x00")
    if recv_exact(conn, 2) != b"\x05\x00":
        raise RuntimeError("SOCKS proxy rejected no-auth greeting")

    host = b"ssh-service.test"
    conn.sendall(b"\x05\x01\x00\x03" + bytes([len(host)]) + host + b"\x00\x16")
    reply = recv_exact(conn, 4)
    if reply[1] != 0:
        raise RuntimeError(f"SOCKS connect failed with status {reply[1]}")
    if reply[3] == 1:
        recv_exact(conn, 4)
    elif reply[3] == 3:
        recv_exact(conn, recv_exact(conn, 1)[0])
    elif reply[3] == 4:
        recv_exact(conn, 16)
    else:
        raise RuntimeError(f"invalid SOCKS address type {reply[3]}")
    recv_exact(conn, 2)

    conn.sendall(b"SSH-2.0-almagest-e2e-client\r\n")
    if recv_exact(conn, 29) != b"SSH-2.0-almagest-e2e-target\r\n":
        raise RuntimeError("unexpected SSH target banner")
PY
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

apply_sniffer_rules() {
	local slot="$1"
	local rules
	printf -v rules 'DIAL\nTCP\nAND\nINTERCEPT\n\nSNIFF HTTP URL:/http-alpha\nROUTE %s DST:127.0.0.1:18081\n\nSNIFF HTTP URL:/http-beta\nROUTE %s DST:127.0.0.1:18082\n\nSNIFF TLS ALPN:http/1.1\nROUTE %s DST:127.0.0.1:18443\n\nSNIFF SSH\nROUTE %s DST:127.0.0.1:18083\n\nTRUE\nDROP' \
		"$slot" "$slot" "$slot" "$slot"
	jq -n --arg rules "$rules" '{"config":{"rules":$rules}}' \
		| cli graph apply-menu --name "Sniffer 2002" >/dev/null
}

port_exists() {
	local node="$1"
	local direction="$2"
	local name="$3"
	local id
	id="$(node_id_by_name "$node")" || return 1
	node_port_id "$id" "$direction" "$name" >/dev/null
}

port_missing() {
	! port_exists "$1" "$2" "$3"
}

http_alpha_pid="$(start_static_http_target 127.0.0.1 18081 "alpha body" "$E2E_RUNTIME/http-alpha.log")"
http_beta_pid="$(start_static_http_target 127.0.0.1 18082 "beta body" "$E2E_RUNTIME/http-beta.log")"
https_pid="$(start_static_https_target 127.0.0.1 18443 "tls body" "$E2E_RUNTIME/https.log" "$E2E_CONFIG_DIR/tls.crt" "$E2E_CONFIG_DIR/tls.key")"
ssh_pid="$(start_static_ssh_target 127.0.0.1 18083 "$E2E_RUNTIME/ssh.log")"

cleanup() {
	stop_static_http_target "$http_alpha_pid"
	stop_static_http_target "$http_beta_pid"
	stop_static_http_target "$https_pid"
	stop_static_http_target "$ssh_pid"
	stop_daemon
}

trap cleanup EXIT

start_daemon
trap cleanup EXIT

wait_graph_node "LocalProxy 2001" || fail "LocalProxy node missing"
wait_graph_node "Sniffer 2002" || fail "Sniffer node missing"
wait_graph_node "OutNetwork 2003" || fail "OutNetwork node missing"

sniffer_id="$(node_id_by_name "Sniffer 2002")"
out_id="$(node_id_by_name "OutNetwork 2003")"
wait_node_label "$sniffer_id" "sniffing 4 classifiers" \
	|| fail "Sniffer did not become ready"
wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:1081" \
	|| fail "local proxy did not start listening"

assert_socks_body "http://service.test/http-alpha" "alpha body"
assert_socks_body "http://service.test/http-beta" "beta body"
assert_curl_fails "http://service.test/rejected"
assert_socks_https_body "https://tls-service.test/secure" "tls body"
retry "$ALMAGEST_E2E_TIMEOUT" "SSH banner through sniffer" assert_socks_ssh \
	|| fail "SSH classifier route failed"

retry "$ALMAGEST_E2E_TIMEOUT" "HTTP alpha access log" \
	grep -F "GET /http-alpha HTTP/1.1" "$E2E_RUNTIME/http-alpha.log" >/dev/null \
	|| fail "HTTP alpha target was not reached"
retry "$ALMAGEST_E2E_TIMEOUT" "HTTP beta access log" \
	grep -F "GET /http-beta HTTP/1.1" "$E2E_RUNTIME/http-beta.log" >/dev/null \
	|| fail "HTTP beta target was not reached"
retry "$ALMAGEST_E2E_TIMEOUT" "HTTPS ALPN access log" \
	grep -F "GET /secure HTTP/1.1 alpn=http/1.1" "$E2E_RUNTIME/https.log" >/dev/null \
	|| fail "HTTPS target did not record expected ALPN"
retry "$ALMAGEST_E2E_TIMEOUT" "SSH access log" \
	grep -F "SSH-2.0-almagest-e2e-client" "$E2E_RUNTIME/ssh.log" >/dev/null \
	|| fail "SSH target did not receive the restored client banner"

# Repeatedly move all routes through sparse dynamic slots. The old slot must
# remain while linked, then disappear as soon as its obsolete link is removed.
current_slot=1
current_link="$(link_id_by_port_names "Sniffer 2002" "slot 1" "OutNetwork 2003" Network)"
for next_slot in 2 4 3; do
	apply_sniffer_rules "$next_slot"
	retry "$ALMAGEST_E2E_TIMEOUT" "Sniffer slot $next_slot port" \
		port_exists "Sniffer 2002" right "slot $next_slot" \
		|| fail "Sniffer did not create slot $next_slot"
	port_exists "Sniffer 2002" right "slot $current_slot" \
		|| fail "Sniffer removed linked obsolete slot $current_slot"
	wait_node_label "$sniffer_id" "waiting for slot $next_slot" \
		|| fail "Sniffer did not wait for slot $next_slot"

	next_link="$(add_link_by_port_name "$sniffer_id" right "slot $next_slot" "$out_id" left Network)"
	wait_node_label "$sniffer_id" "sniffing 4 classifiers" \
		|| fail "Sniffer did not become ready on slot $next_slot"
	assert_socks_body "http://service.test/http-alpha" "alpha body"
	assert_socks_body "http://service.test/http-beta" "beta body"
	assert_socks_https_body "https://tls-service.test/secure" "tls body"
	retry "$ALMAGEST_E2E_TIMEOUT" "SSH banner through slot $next_slot" assert_socks_ssh \
		|| fail "SSH classifier route failed on slot $next_slot"

	cli graph delete-link --id "$current_link" >/dev/null
	retry "$ALMAGEST_E2E_TIMEOUT" "unused Sniffer slot $current_slot removed immediately" \
		port_missing "Sniffer 2002" right "slot $current_slot" \
		|| fail "Sniffer kept unused slot $current_slot after unlink"
	assert_socks_body "http://service.test/http-alpha" "alpha body"

	current_slot="$next_slot"
	current_link="$next_link"
done

# A slot that is still present in the rules must remain after detach. The
# Sniffer must stop using its old backend and recover after the link returns.
cli graph delete-link --id "$current_link" >/dev/null
port_exists "Sniffer 2002" right "slot $current_slot" \
	|| fail "Sniffer removed rule-owned slot $current_slot"
wait_node_label "$sniffer_id" "waiting for slot $current_slot" \
	|| fail "Sniffer did not report its detached active slot"
assert_curl_fails "http://service.test/http-alpha"
add_link_by_port_name "$sniffer_id" right "slot $current_slot" "$out_id" left Network >/dev/null
wait_node_label "$sniffer_id" "sniffing 4 classifiers" \
	|| fail "Sniffer did not recover after backend reattach"
assert_socks_body "http://service.test/http-alpha" "alpha body"
assert_socks_https_body "https://tls-service.test/secure" "tls body"
retry "$ALMAGEST_E2E_TIMEOUT" "SSH banner after backend reattach" assert_socks_ssh \
	|| fail "SSH classifier route did not recover"

log "case passed"
