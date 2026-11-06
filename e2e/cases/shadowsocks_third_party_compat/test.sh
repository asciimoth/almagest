#!/usr/bin/env bash
set -euo pipefail
# shellcheck disable=SC1091
source /e2e/lib/common.sh

password=reference-compat
target_body='Shadowsocks reference compatibility'
target_pid=''
ss_local_pid=''
ss_server_pid=''
ss_tunnel_pid=''
gost_pid=''
dnsmasq_pid=''
cleanup() {
 if [[ -n "$dnsmasq_pid" ]]; then kill "$dnsmasq_pid" 2>/dev/null || true; wait "$dnsmasq_pid" 2>/dev/null || true; fi
 if [[ -n "$gost_pid" ]]; then kill "$gost_pid" 2>/dev/null || true; wait "$gost_pid" 2>/dev/null || true; fi
 if [[ -n "$ss_tunnel_pid" ]]; then kill "$ss_tunnel_pid" 2>/dev/null || true; wait "$ss_tunnel_pid" 2>/dev/null || true; fi
 if [[ -n "$ss_local_pid" ]]; then kill "$ss_local_pid" 2>/dev/null || true; wait "$ss_local_pid" 2>/dev/null || true; fi
 if [[ -n "$ss_server_pid" ]]; then kill "$ss_server_pid" 2>/dev/null || true; wait "$ss_server_pid" 2>/dev/null || true; fi
 stop_static_http_target "$target_pid"
 stop_daemon
}
trap cleanup EXIT

target_pid="$(start_static_http_target 127.0.0.1 18090 "$target_body" "$E2E_RUNTIME/target.log")"
cat >"$E2E_RUNTIME/dnsmasq.conf" <<EOF
port=11553
listen-address=127.0.0.1
bind-interfaces
no-resolv
no-hosts
address=/shadowsocks.test/127.0.0.42
EOF
dnsmasq --no-daemon --conf-file="$E2E_RUNTIME/dnsmasq.conf" >"$E2E_RUNTIME/dnsmasq.log" 2>&1 &
dnsmasq_pid=$!
ss-server -s 127.0.0.1 -p 11091 -k "$password" -m aes-256-gcm -u >"$E2E_RUNTIME/ss-server.log" 2>&1 &
ss_server_pid=$!
start_daemon
wait_graph_node 'Almagest SS server' || fail 'Shadowsocks server node missing'
wait_graph_node 'Almagest SS client' || fail 'Shadowsocks client node missing'
wait_graph_node 'Almagest SS loop client' || fail 'loop Shadowsocks client node missing'
wait_node_label "$(node_id_by_name 'Almagest SS server')" '127.0.0.1:11090' || fail 'Almagest Shadowsocks server did not start'
wait_node_label "$(node_id_by_name 'Almagest SS client')" '127.0.0.1:11091' || fail 'Almagest Shadowsocks client did not attach upstream'
wait_node_label "$(node_id_by_name 'Almagest SS loop client')" '127.0.0.1:11090' || fail 'Almagest loop Shadowsocks client did not attach upstream'
wait_log_contains 'almagest/LocalProxy' 'socks listening on 127.0.0.1:11096' \
 || fail 'loop local SOCKS listener did not start'
curl --fail --silent --show-error --connect-timeout 2 --max-time 5 \
 --socks5-hostname 127.0.0.1:11096 http://127.0.0.1:18090/almagest-loop \
 --output "$E2E_RUNTIME/almagest-loop.txt" \
 || fail 'first request through Almagest client and server failed'
[[ "$(cat "$E2E_RUNTIME/almagest-loop.txt")" == "$target_body" ]] || fail 'Almagest loop response mismatch'
ss-local -s 127.0.0.1 -p 11090 -l 11093 -k "$password" -m aes-256-gcm -u >"$E2E_RUNTIME/ss-local.log" 2>&1 &
ss_local_pid=$!
ss-tunnel -s 127.0.0.1 -p 11090 -l 11094 -L 127.0.0.1:11553 -k "$password" -m aes-256-gcm -U >"$E2E_RUNTIME/ss-tunnel.log" 2>&1 &
ss_tunnel_pid=$!
gost -L 'udp://127.0.0.1:11095/localhost:11553?ttl=10' -F 'socks5://127.0.0.1:11092' >"$E2E_RUNTIME/gost.log" 2>&1 &
gost_pid=$!

reference_client_get() {
 curl --fail --silent --show-error --connect-timeout 2 --max-time 5 \
  --socks5-hostname 127.0.0.1:11093 http://127.0.0.1:18090/reference-client \
  --output "$E2E_RUNTIME/reference-client.txt"
}
almagest_client_get() {
 curl --fail --silent --show-error --connect-timeout 2 --max-time 5 \
  --socks5-hostname 127.0.0.1:11092 http://127.0.0.1:18090/almagest-client \
  --output "$E2E_RUNTIME/almagest-client.txt"
}
retry "$ALMAGEST_E2E_TIMEOUT" 'reference client to Almagest server' reference_client_get \
 || fail "reference client failed: $(tail -20 "$E2E_RUNTIME/ss-local.log")"
[[ "$(cat "$E2E_RUNTIME/reference-client.txt")" == "$target_body" ]] || fail 'reference client response mismatch'
retry "$ALMAGEST_E2E_TIMEOUT" 'Almagest client to reference server' almagest_client_get \
 || fail "Almagest client failed: $(tail -20 "$E2E_RUNTIME/ss-server.log")"
[[ "$(cat "$E2E_RUNTIME/almagest-client.txt")" == "$target_body" ]] || fail 'Almagest client response mismatch'

dns_answers() { dig +short +time=2 +tries=1 @127.0.0.1 -p "$1" shadowsocks.test A | grep -Fx 127.0.0.42 >/dev/null; }
reference_udp_answers() { dns_answers 11094; }
almagest_udp_answers() { dns_answers 11095; }
retry "$ALMAGEST_E2E_TIMEOUT" 'reference UDP client to Almagest server' reference_udp_answers \
 || fail "reference UDP failed: $(tail -20 "$E2E_RUNTIME/ss-tunnel.log")"
retry "$ALMAGEST_E2E_TIMEOUT" 'Almagest UDP client to reference server' almagest_udp_answers \
 || fail "Almagest UDP failed: $(tail -20 "$E2E_RUNTIME/gost.log")"

grep -F 'GET /reference-client' "$E2E_RUNTIME/target.log" >/dev/null || fail 'reference request did not reach target'
grep -F 'GET /almagest-client' "$E2E_RUNTIME/target.log" >/dev/null || fail 'Almagest request did not reach target'
grep -F 'GET /almagest-loop' "$E2E_RUNTIME/target.log" >/dev/null || fail 'Almagest loop request did not reach target'
log 'case passed'
