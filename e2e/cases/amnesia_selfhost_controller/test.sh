#!/usr/bin/env bash
set -euo pipefail

: "${ALMAGEST_E2E_TIMEOUT:=60}"
source /e2e/lib/common.sh
source "$E2E_CONFIG_DIR/amnesia.env"

ALMAGEST_E2E_TMP_FILE_STORAGE=0
ALMAGEST_E2E_FILE_STORAGE="/root/almagest-e2e-store/$E2E_CASE"

upstream_iface="wg0"
upstream_pid=""
gateway_pid=""
proxy_pid=""
http_pid=""
gateway_count="$E2E_RUNTIME/gateway-requests.log"

cleanup() {
	trap - EXIT
	stop_static_http_target "$http_pid"
	for pid in "$proxy_pid" "$gateway_pid" "$upstream_pid"; do
		if [[ -n "$pid" ]]; then
			kill "$pid" 2>/dev/null || true
			wait "$pid" 2>/dev/null || true
		fi
	done
	ip link del "$upstream_iface" 2>/dev/null || true
	stop_daemon
}
trap cleanup EXIT

uapi_set() {
	local payload="$1"
	local reply
	reply="$(printf '%s\n\n' "$payload" | socat - "UNIX-CONNECT:/var/run/amneziawg/$upstream_iface.sock")"
	printf '%s\n\n' "$reply" >>"$E2E_RUNTIME/amnezia-uapi.log"
	grep -q '^errno=0$' <<<"$reply" || fail "amneziawg-go UAPI request failed: $reply"
}

start_upstream() {
	mkdir -p /var/run/amneziawg
	WG_PROCESS_FOREGROUND=1 LOG_LEVEL=verbose amneziawg-go -f "$upstream_iface" \
		>"$E2E_RUNTIME/amneziawg-go.log" 2>&1 &
	upstream_pid="$!"
	retry "$ALMAGEST_E2E_TIMEOUT" "amneziawg-go UAPI" \
		test -S "/var/run/amneziawg/$upstream_iface.sock" \
		|| fail "amneziawg-go did not create its UAPI socket"
	ip addr replace 10.205.20.1/32 dev "$upstream_iface"
	ip link set dev "$upstream_iface" mtu 1420 up
	ip route replace 10.205.10.1/32 dev "$upstream_iface"
	uapi_set "$(cat <<EOF
set=1
private_key=$SERVER_PRIVATE_HEX
listen_port=51861
jc=2
jmin=11
jmax=23
s1=12
s2=12
s3=12
s4=12
h1=6111
h2=6222
h3=6333
h4=6444
i1=<b 0xaa55><rc 3><rd 2><t>
i2=<r 5>
i3=<rd 4>
i4=<rc 6>
i5=<b 0x01020304>
header_protection_key=$HEADER_KEY
content_padding_addition=4-16
rekey_after_time=90-120
rekey_timeout=5-8
reject_after_time=180-220
keepalive_timeout=10-15
max_handshake_attempts=3-5
random_trailers=true
disable_cookies=true
replace_peers=true
public_key=$CLIENT_PUBLIC_HEX
protocol_version=1
replace_allowed_ips=true
allowed_ip=10.205.10.1/32
endpoint=127.0.0.1:51860
persistent_keepalive_interval=1
EOF
)"
}

start_gateway() {
	amnesia-e2e-tool serve-gateway -listen 127.0.0.1:18090 \
		-private-key "$E2E_CONFIG_DIR/gateway-private.pem" \
		-config-file "$E2E_CONFIG_DIR/gateway-profile.vpn" \
		>"$E2E_RUNTIME/gateway.log" 2>&1 &
	gateway_pid="$!"
	retry "$ALMAGEST_E2E_TIMEOUT" "mock Amnezia gateway" \
		curl -fsS http://127.0.0.1:18090/health >/dev/null \
		|| fail "mock Amnezia gateway did not start"

	: >"$gateway_count"
	python3 -u - 18490 18090 "$E2E_CONFIG_DIR/gateway.pem" \
		"$E2E_CONFIG_DIR/gateway.key" "$gateway_count" <<'PY' \
		>"$E2E_RUNTIME/gateway-proxy.log" 2>&1 &
import http.client
import http.server
import ssl
import sys

listen_port, upstream_port = map(int, sys.argv[1:3])
cert, key, count_path = sys.argv[3:]

class Handler(http.server.BaseHTTPRequestHandler):
    def forward(self):
        size = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(size)
        headers = {k: v for k, v in self.headers.items()
                   if k.lower() not in {"host", "connection", "content-length"}}
        upstream = http.client.HTTPConnection("127.0.0.1", upstream_port, timeout=15)
        upstream.request(self.command, self.path, body=body, headers=headers)
        response = upstream.getresponse()
        data = response.read()
        if self.command == "POST":
            with open(count_path, "a", encoding="utf-8") as output:
                output.write(self.path + "\n")
        self.send_response(response.status)
        for name, value in response.getheaders():
            if name.lower() not in {"connection", "content-length", "transfer-encoding"}:
                self.send_header(name, value)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)
        upstream.close()

    do_GET = forward
    do_POST = forward

    def log_message(self, fmt, *args):
        return

server = http.server.ThreadingHTTPServer(("127.0.0.1", listen_port), Handler)
context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
context.load_cert_chain(cert, key)
server.socket = context.wrap_socket(server.socket, server_side=True)
server.serve_forever()
PY
	proxy_pid="$!"
	retry "$ALMAGEST_E2E_TIMEOUT" "HTTPS Amnezia gateway" \
		curl -fsS --cacert "$E2E_CONFIG_DIR/amnesia-root.pem" \
		https://gateway.amnesia.test:18490/health >/dev/null \
		|| fail "HTTPS Amnezia gateway did not start"
}

menu_field() {
	local node="$1"
	local block="$2"
	local field="$3"
	cli graph menu --node "$node" | jq -er --arg block "$block" --arg field "$field" '
		.blocks[] | select(.id == $block) | .items[] | select(.id == $field) | (.value // .text // "")
	'
}

gateway_requests() {
	wc -l <"$gateway_count" | tr -d ' '
}

gateway_requests_greater_than() {
	local previous="$1"
	(( $(gateway_requests) > previous ))
}

config_saves() {
	grep -c 'config saved' "$E2E_DAEMON_LOG" || true
}

config_saves_greater_than() {
	local previous="$1"
	(( $(config_saves) > previous ))
}

server_ping_works() {
	ping -I "$upstream_iface" -c 1 -W 1 10.205.10.1 >/dev/null 2>&1
}

server_ping_fails() {
	! server_ping_works
}

http_client_ready() {
	local node="$1"
	[[ "$(menu_field "$node" result status)" == "Status: 200 OK" \
		&& -z "$(menu_field "$node" result error)" \
		&& "$(menu_field "$node" result response)" == "amnesia controller e2e" ]]
}

assert_tunnel_works() {
	local client="$1"
	retry "$ALMAGEST_E2E_TIMEOUT" "server-to-client AmneziaWG ping" server_ping_works \
		|| fail "server could not ping the Almagest tunnel"
	cli graph click-menu-button --node "$client" --block request --button send >/dev/null
	retry "$ALMAGEST_E2E_TIMEOUT" "client-to-server AmneziaWG HTTP" http_client_ready "$client" \
		|| fail "Almagest HTTP client could not use the AmneziaWG tunnel"
}

apply_amnesia_config() {
	local node="$1"
	local json="$2"
	cli graph apply-menu --node "$node" --json "$json" >/dev/null
}

start_upstream
start_gateway
http_pid="$(start_static_http_target 10.205.20.1 18085 "amnesia controller e2e" "$E2E_RUNTIME/upstream-http.log")"
start_daemon

wait_node_class_available "almagest/AmnesiaCtrl" || fail "AmnesiaCtrl node class is unavailable"
wait_graph_node "Amnesia Service" || fail "AmnesiaCtrl node is missing"
amnesia_node="$(node_id_by_name "Amnesia Service")"
wireguard_node="$(node_id_by_name "WireGuard Amnesia")"
http_client="$(node_id_by_name "HTTP Client Amnesia")"

cli tls set-cert --name amnesia-root --pem-file "$E2E_CONFIG_DIR/amnesia-root.pem" >/dev/null
cli tls set-cert --name wrong-root --pem-file "$E2E_CONFIG_DIR/wrong-root.pem" >/dev/null
cli tls set-policy --name amnesia-tls \
	--spec '{"trust_anchors":[{"cert":"amnesia-root","scope":{"any":true}}]}' >/dev/null
cli secrets set --key amnesia-activation --value "$(cat "$E2E_CONFIG_DIR/activation-one.vpn")" >/dev/null
cli secrets set-nodes --key amnesia-activation --node "Amnesia Service" >/dev/null
cli secrets set --key amnesia-gateway-public --value "$(cat "$E2E_CONFIG_DIR/gateway-public.pem")" >/dev/null
cli secrets set-nodes --key amnesia-gateway-public --node "Amnesia Service" >/dev/null

wait_node_label "$wireguard_node" up || fail "WireGuard node did not become ready"
add_link_by_port_name "$amnesia_node" right WireGuard "$wireguard_node" left WGCtrl >/dev/null
wait_node_label "$amnesia_node" up || fail "AmnesiaCtrl node did not import the service profile"
[[ "$(menu_field "$amnesia_node" status inputFormat)" == "service-key" ]] \
	|| fail "Amnesia status did not report service-key input"
[[ "$(menu_field "$amnesia_node" status peerTransport)" == "WireGuard default" ]] \
	|| fail "Amnesia peer did not report the WireGuard default UDP transport"
[[ "$(menu_field "$amnesia_node" status uuidPersistent)" == "true" ]] \
	|| fail "Amnesia installation UUID was not persistent"
assert_tunnel_works "$http_client"

# The controller link owns the imported peer and supports a fresh attachment.
wg_link="$(link_id_by_port_names "Amnesia Service" WireGuard "WireGuard Amnesia" WGCtrl)"
cli graph delete-link --id "$wg_link" >/dev/null
wait_node_label "$amnesia_node" "waiting for WireGuard" || fail "Amnesia did not observe controller detach"
retry 8 "peer removal after controller detach" server_ping_fails \
	|| fail "tunnel remained active after controller detach"
add_link_by_port_name "$amnesia_node" right WireGuard "$wireguard_node" left WGCtrl >/dev/null
wait_node_label "$amnesia_node" up || fail "Amnesia did not recover after controller reattach"
assert_tunnel_works "$http_client"

# Service API traffic requires the Network port. Its detach also removes the
# tracked peer, while the WireGuard node keeps its independent UDP network.
api_link="$(link_id_by_port_names "Amnesia Service" Network "OutNetwork API" Network)"
cli graph delete-link --id "$api_link" >/dev/null
wait_node_label "$amnesia_node" "waiting for API network" || fail "Amnesia did not observe API network detach"
retry 8 "peer removal after API network detach" server_ping_fails \
	|| fail "tunnel remained active after API network detach"
api_node="$(node_id_by_name "OutNetwork API")"
add_link_by_port_name "$amnesia_node" right Network "$api_node" left Network >/dev/null
wait_node_label "$amnesia_node" up || fail "Amnesia did not recover after API network reattach"
assert_tunnel_works "$http_client"

# Replacing the user-provided activation-key secret causes a new negotiation.
previous_requests="$(gateway_requests)"
cli secrets set --key amnesia-activation --value "$(cat "$E2E_CONFIG_DIR/activation-two.vpn")" >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "gateway request after activation-key change" \
	gateway_requests_greater_than "$previous_requests" \
	|| fail "activation-key secret change did not cause a new import"
wait_node_label "$amnesia_node" up || fail "Amnesia did not recover after activation-key change"
assert_tunnel_works "$http_client"

# Replacing the selected TLS policy must rebuild the HTTPS client. An invalid
# trust root is a node error and removes the old tracked peer.
cli tls set-policy --name amnesia-tls \
	--spec '{"trust_anchors":[{"cert":"wrong-root","scope":{"any":true}}]}' >/dev/null
wait_node_label "$amnesia_node" error || fail "bad TLS policy did not put Amnesia in error"
[[ -n "$(menu_field "$amnesia_node" status error)" ]] || fail "TLS error was absent from node status"
retry 8 "peer removal after TLS failure" server_ping_fails \
	|| fail "tunnel remained active after TLS failure"
previous_requests="$(gateway_requests)"
cli tls set-policy --name amnesia-tls \
	--spec '{"trust_anchors":[{"cert":"amnesia-root","scope":{"any":true}}]}' >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "gateway request after TLS recovery" \
	gateway_requests_greater_than "$previous_requests" \
	|| fail "restored TLS policy did not cause a new import"
wait_node_label "$amnesia_node" up || fail "Amnesia did not recover after TLS policy restore"
assert_tunnel_works "$http_client"

# A tunnel selection parameter is material configuration and causes another
# import without changing either graph link.
previous_requests="$(gateway_requests)"
apply_amnesia_config "$amnesia_node" '{"config":{"serverCountryCode":"nl"}}'
retry "$ALMAGEST_E2E_TIMEOUT" "gateway request after country change" \
	gateway_requests_greater_than "$previous_requests" \
	|| fail "country change did not cause a new import"
wait_node_label "$amnesia_node" up || fail "Amnesia did not recover after country change"
[[ "$(menu_field "$amnesia_node" status country)" == "nl" ]] \
	|| fail "Amnesia status did not report the selected country"
assert_tunnel_works "$http_client"

storage_id="$(cli kv entries --path nodes/amnesia | jq -er '
	[.entries[] | select(.kind == "bucket") | .name]
	| if length == 1 then .[0] else error("expected one Amnesia KV subtree") end
')" || fail "could not identify the Amnesia KV subtree"
uuid_before="$(cli kv get --path "nodes/amnesia/$storage_id" --key installation_uuid | jq -er '.value.data')" \
	|| fail "persistent installation UUID was absent before restart"
[[ -n "$uuid_before" ]] || fail "persistent installation UUID is empty"
# Restore all WireGuard links after its startup. This avoids calling link
# callbacks while WireGuard is still initializing and tests fresh attachments
# after persisted graph state is loaded.
previous_saves="$(config_saves)"
wg_link="$(link_id_by_port_names "Amnesia Service" WireGuard "WireGuard Amnesia" WGCtrl)"
cli graph delete-link --id "$wg_link" >/dev/null
wait_node_label "$amnesia_node" "waiting for WireGuard" || fail "Amnesia did not detach before restart"
udp_link="$(link_id_by_port_names "WireGuard Amnesia" Network "OutNetwork UDP" Network)"
tun_link="$(link_id_by_port_names "VTun Amnesia" Tun "WireGuard Amnesia" Tun)"
cli graph delete-link --id "$udp_link" >/dev/null
cli graph delete-link --id "$tun_link" >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "detached graph config autosave" \
	config_saves_greater_than "$previous_saves" \
	|| fail "detached graph state was not saved before restart"
stop_daemon
start_daemon
amnesia_node_after="$(node_id_by_name "Amnesia Service")"
wireguard_node_after="$(node_id_by_name "WireGuard Amnesia")"
http_client_after="$(node_id_by_name "HTTP Client Amnesia")"
udp_node_after="$(node_id_by_name "OutNetwork UDP")"
vtun_node_after="$(node_id_by_name "VTun Amnesia")"
add_link_by_port_name "$wireguard_node_after" right Network "$udp_node_after" left Network >/dev/null
add_link_by_port_name "$vtun_node_after" right Tun "$wireguard_node_after" left Tun >/dev/null
wait_node_label "$wireguard_node_after" up || fail "WireGuard node did not recover after daemon restart"
add_link_by_port_name "$amnesia_node_after" right WireGuard "$wireguard_node_after" left WGCtrl >/dev/null
wait_node_label "$amnesia_node_after" up || fail "Amnesia did not recover after daemon restart"
storage_id_after="$(cli kv entries --path nodes/amnesia | jq -er '
	[.entries[] | select(.kind == "bucket") | .name]
	| if length == 1 then .[0] else error("expected one Amnesia KV subtree") end
')" || fail "Amnesia created a second KV subtree after restart"
[[ "$storage_id_after" == "$storage_id" ]] \
	|| fail "Amnesia KV subtree changed across daemon restart"
uuid_after="$(cli kv get --path "nodes/amnesia/$storage_id_after" --key installation_uuid | jq -er '.value.data')" \
	|| fail "persistent installation UUID was absent after restart"
[[ "$uuid_after" == "$uuid_before" ]] \
	|| fail "installation UUID changed across daemon restart: $uuid_before -> $uuid_after"
assert_tunnel_works "$http_client_after"

log "case passed"
