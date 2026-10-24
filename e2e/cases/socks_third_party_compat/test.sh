#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

target_body="Almagest SOCKS third-party compatibility"
target_log="$E2E_RUNTIME/target.log"
target_pid=""
dnsmasq_pid=""
gost_pids=()
resolv_backup="$E2E_RUNTIME/resolv.conf.original"

cleanup() {
	for pid in "${gost_pids[@]:-}"; do
		kill "$pid" 2>/dev/null || true
		wait "$pid" 2>/dev/null || true
	done
	if [[ -n "$dnsmasq_pid" ]]; then
		kill "$dnsmasq_pid" 2>/dev/null || true
		wait "$dnsmasq_pid" 2>/dev/null || true
	fi
	stop_static_http_target "$target_pid"
	stop_daemon
	if [[ -f "$resolv_backup" ]]; then
		cp -a "$resolv_backup" /etc/resolv.conf
	fi
}

curl_proxy() {
	local proxy="$1"
	local path="$2"
	local output="$3"
	curl --fail --silent --show-error --connect-timeout 2 --max-time 8 \
		--proxy "$proxy" "http://compat.test:18080$path" --output "$output"
}

assert_body() {
	local file="$1"
	local got
	got="$(cat "$file")"
	[[ "$got" == "$target_body" ]] || fail "unexpected target response: $got"
}

start_gost_forward() {
	local name="$1"
	local port="$2"
	local chain="$3"
	local log_file="$E2E_RUNTIME/gost-$name.log"

	gost -L "tcp://127.0.0.1:$port/127.0.0.1:18080" -F "$chain" \
		>"$log_file" 2>&1 &
	local pid="$!"
	gost_pids+=("$pid")
	retry "$ALMAGEST_E2E_TIMEOUT" "Gost $name listener" \
		gost_forward_listens "$port" \
		|| fail "Gost $name did not listen; $(cat "$log_file")"
}

gost_forward_listens() {
	local port="$1"
	ss -H -ltn "sport = :$port" | grep -q .
}

start_gost() {
	local name="$1"
	shift
	local log_file="$E2E_RUNTIME/gost-$name.log"

	gost "$@" >"$log_file" 2>&1 &
	gost_pids+=("$!")
}

trap cleanup EXIT
cp -a /etc/resolv.conf "$resolv_backup"
cat >"$E2E_RUNTIME/dnsmasq.conf" <<EOF
port=53
listen-address=127.0.0.54
bind-interfaces
no-resolv
no-hosts
log-queries
address=/compat.test/127.0.0.1
ptr-record=8.8.8.8.in-addr.arpa,compat.test
EOF
dnsmasq --no-daemon --conf-file="$E2E_RUNTIME/dnsmasq.conf" \
	>"$E2E_RUNTIME/dnsmasq.log" 2>&1 &
dnsmasq_pid="$!"
printf 'nameserver 127.0.0.54\noptions timeout:1 attempts:1\n' >/etc/resolv.conf
# The retry helper invokes this function by name.
# shellcheck disable=SC2329
compat_dns_answers() {
	dig +short compat.test A @127.0.0.54 | grep -Fx 127.0.0.1 >/dev/null \
		&& dig +short -x 8.8.8.8 @127.0.0.54 | grep -Fx compat.test. >/dev/null
}
retry "$ALMAGEST_E2E_TIMEOUT" "compatibility DNS" \
	compat_dns_answers \
	|| fail "compatibility DNS did not start"
target_pid="$(start_static_http_target 127.0.0.1 18080 "$target_body" "$target_log")"
start_daemon
trap cleanup EXIT

wait_graph_node "SOCKS Compat Anonymous" || fail "anonymous SOCKS server node missing"
wait_graph_node "SOCKS Compat Auth" || fail "authenticated SOCKS server node missing"
wait_graph_node "SOCKS Compat TLS" || fail "TLS SOCKS server node missing"
wait_graph_node "TLS Compat Listener" || fail "TLS listener wrapper node missing"
wait_log_contains "almagest/SocksServer" "socks listening on 127.0.0.1:11080" \
	|| fail "anonymous SOCKS listener did not start"
wait_log_contains "almagest/SocksServer" "socks+ws listening on 127.0.0.1:11081/ws" \
	|| fail "SOCKS WebSocket listener did not start"
wait_log_contains "almagest/SocksServer" "socks listening on 127.0.0.1:11082" \
	|| fail "authenticated SOCKS listener did not start"
wait_log_contains "almagest/SocksServer" "socks listening on 127.0.0.1:11083" \
	|| fail "TLS SOCKS listener did not start"
wait_log_contains "almagest/SocksServer" "socks+ws listening on 127.0.0.1:11084/socks" \
	|| fail "SOCKS WSS listener did not start"

declare -A curl_proxies=(
	[socks4]="socks4://127.0.0.1:11080"
	[socks4a]="socks4a://127.0.0.1:11080"
	[socks5]="socks5://127.0.0.1:11080"
	[socks5h]="socks5h://127.0.0.1:11080"
	[socks5-auth]="socks5://compat:socksgo-compat@127.0.0.1:11082"
	[socks5h-auth]="socks5h://compat:socksgo-compat@127.0.0.1:11082"
)

for name in socks4 socks4a socks5 socks5h socks5-auth socks5h-auth; do
	out="$E2E_RUNTIME/curl-$name.txt"
	retry "$ALMAGEST_E2E_TIMEOUT" "curl $name compatibility" \
		curl_proxy "${curl_proxies[$name]}" "/curl-$name" "$out" \
		|| fail "curl $name did not connect through Almagest"
	assert_body "$out"
done

if curl_proxy "socks5://compat:wrong@127.0.0.1:11082" /wrong-password \
	"$E2E_RUNTIME/wrong-password.txt" 2>/dev/null; then
	fail "authenticated SOCKS server accepted a wrong password"
fi

if ! resolved5="$(tor-resolve -5 compat.test 127.0.0.1:11080)"; then
	fail "Tor SOCKS5 RESOLVE failed"
fi
[[ "$resolved5" == "127.0.0.1" ]] \
	|| fail "Tor SOCKS5 RESOLVE returned $resolved5"
if ! resolved4="$(tor-resolve -4 compat.test 127.0.0.1:11080)"; then
	fail "Tor SOCKS4a RESOLVE failed"
fi
[[ "$resolved4" == "127.0.0.1" ]] \
	|| fail "Tor SOCKS4a RESOLVE returned $resolved4"
if ! reverse="$(tor-resolve -5 -x 8.8.8.8 127.0.0.1:11080)"; then
	fail "Tor SOCKS5 RESOLVE_PTR failed; DNS log: $(tail -20 "$E2E_RUNTIME/dnsmasq.log")"
fi
[[ "$reverse" == "compat.test" || "$reverse" == "compat.test." ]] \
	|| fail "Tor SOCKS5 RESOLVE_PTR returned $reverse"

start_gost_forward plain 18100 "socks5://127.0.0.1:11080"
retry "$ALMAGEST_E2E_TIMEOUT" "Gost plain SOCKS5 request" \
	curl --fail --silent --show-error --max-time 8 \
		http://127.0.0.1:18100/gost-plain --output "$E2E_RUNTIME/gost-plain.txt" \
	|| fail "Gost plain SOCKS5 chain did not reach the target"
assert_body "$E2E_RUNTIME/gost-plain.txt"

# GOST defaults its WebSocket path to /ws. Almagest defaults to /socks, so
# this listener is configured with /ws to verify the unqualified GOST URL.
start_gost_forward websocket 18101 "socks5+ws://127.0.0.1:11081"
retry "$ALMAGEST_E2E_TIMEOUT" "Gost SOCKS5 WebSocket request" \
	curl --fail --silent --show-error --max-time 8 \
		http://127.0.0.1:18101/gost-websocket --output "$E2E_RUNTIME/gost-websocket.txt" \
	|| fail "Gost SOCKS5 WebSocket chain did not reach the target"
assert_body "$E2E_RUNTIME/gost-websocket.txt"

start_gost_forward tls 18102 "socks5+tls://127.0.0.1:11083"
retry "$ALMAGEST_E2E_TIMEOUT" "Gost SOCKS5 TLS request" \
	curl --fail --silent --show-error --max-time 8 \
		http://127.0.0.1:18102/gost-tls --output "$E2E_RUNTIME/gost-tls.txt" \
	|| fail "Gost SOCKS5 TLS chain did not reach the target"
assert_body "$E2E_RUNTIME/gost-tls.txt"

# The TLS server omits wsPath and therefore uses Almagest's /socks default.
# Override GOST's /ws default to verify that the node default is usable.
start_gost_forward wss 18103 "socks5+wss://127.0.0.1:11084?path=/socks"
retry "$ALMAGEST_E2E_TIMEOUT" "Gost SOCKS5 WSS request" \
	curl --fail --silent --show-error --max-time 8 \
		http://127.0.0.1:18103/gost-wss --output "$E2E_RUNTIME/gost-wss.txt" \
	|| fail "Gost SOCKS5 WSS chain did not reach the target; $(tail -20 "$E2E_RUNTIME/gost-wss.log")"
assert_body "$E2E_RUNTIME/gost-wss.txt"

# A UDP local forward with a GOST SOCKS5 chain uses the GOST UDP-over-TCP
# extension instead of a standard UDP ASSOCIATE relay.
start_gost udp-tunnel \
	-L "udp://127.0.0.1:18110/127.0.0.54:53?ttl=10" \
	-F "socks5://127.0.0.1:11080"
# The retry helper invokes this function by name.
# shellcheck disable=SC2329
gost_udp_dns_answers() {
	dig +short +time=2 +tries=1 @127.0.0.1 -p 18110 compat.test A \
		| grep -Fx 127.0.0.1 >/dev/null
}
retry "$ALMAGEST_E2E_TIMEOUT" "Gost UDP-over-TCP DNS request" \
	gost_udp_dns_answers \
	|| fail "Gost UDP-over-TCP chain did not return the DNS answer"

# mbind=true makes remote TCP forwarding use one multiplexed GOST BIND
# session. The bound port is owned by the Almagest SOCKS listener.
start_gost mux-bind \
	-L "rtcp://127.0.0.1:18120/127.0.0.1:18080" \
	-F "socks5://127.0.0.1:11080?mbind=true"
retry "$ALMAGEST_E2E_TIMEOUT" "Gost multiplexed remote BIND request" \
	curl --fail --silent --show-error --max-time 8 \
		http://127.0.0.1:18120/gost-mux-bind \
		--output "$E2E_RUNTIME/gost-mux-bind.txt" \
	|| fail "Gost multiplexed remote BIND did not reach the target"
assert_body "$E2E_RUNTIME/gost-mux-bind.txt"

for path in curl-socks4 curl-socks4a curl-socks5 curl-socks5h \
	curl-socks5-auth curl-socks5h-auth gost-plain gost-websocket gost-tls \
	gost-wss gost-mux-bind; do
	grep -F "GET /$path" "$target_log" >/dev/null \
		|| fail "target did not receive /$path"
done

log "case passed"
