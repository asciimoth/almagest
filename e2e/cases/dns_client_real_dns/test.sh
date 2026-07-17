#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

dnsmasq_pid=""
dot_pid=""

cleanup() {
	if [[ -n "$dot_pid" ]]; then
		kill "$dot_pid" 2>/dev/null || true
		wait "$dot_pid" 2>/dev/null || true
	fi
	if [[ -n "$dnsmasq_pid" ]]; then
		kill "$dnsmasq_pid" 2>/dev/null || true
		wait "$dnsmasq_pid" 2>/dev/null || true
	fi
	stop_daemon
}

start_dnsmasq() {
	local conf="$E2E_RUNTIME/dnsmasq.conf"
	cat >"$conf" <<'EOF'
port=5353
listen-address=127.0.0.1
bind-interfaces
no-resolv
no-hosts
address=/regular.e2e.test/198.51.100.10
address=/dot.e2e.test/198.51.100.20
EOF
	dnsmasq --no-daemon --conf-file="$conf" >"$E2E_RUNTIME/dnsmasq.log" 2>&1 &
	dnsmasq_pid="$!"
	retry "$ALMAGEST_E2E_TIMEOUT" "dnsmasq TCP listener" nc -z 127.0.0.1 5353 \
		|| fail "dnsmasq did not listen on 127.0.0.1:5353"
}

install_dot_ca_and_start_wrapper() {
	local ca_key="$E2E_RUNTIME/dot-ca.key"
	local ca_crt="$E2E_RUNTIME/dot-ca.crt"
	local srv_key="$E2E_RUNTIME/dot.key"
	local srv_csr="$E2E_RUNTIME/dot.csr"
	local srv_crt="$E2E_RUNTIME/dot.crt"
	local ext="$E2E_RUNTIME/dot.ext"

	openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
		-keyout "$ca_key" -out "$ca_crt" \
		-subj "/CN=Almagest E2E DNS CA" >/dev/null 2>&1
	openssl req -newkey rsa:2048 -nodes \
		-keyout "$srv_key" -out "$srv_csr" \
		-subj "/CN=dot-upstream.e2e.test" >/dev/null 2>&1
	cat >"$ext" <<'EOF'
subjectAltName=DNS:dot-upstream.e2e.test
extendedKeyUsage=serverAuth
EOF
	openssl x509 -req -in "$srv_csr" -CA "$ca_crt" -CAkey "$ca_key" \
		-CAcreateserial -out "$srv_crt" -days 1 -sha256 -extfile "$ext" >/dev/null 2>&1

	cp "$ca_crt" /usr/local/share/ca-certificates/almagest-e2e-dns-ca.crt
	update-ca-certificates >/dev/null

	socat \
		OPENSSL-LISTEN:5853,bind=127.0.0.1,reuseaddr,fork,cert="$srv_crt",key="$srv_key",verify=0 \
		TCP:127.0.0.1:5353 \
		>"$E2E_RUNTIME/dot-socat.log" 2>&1 &
	dot_pid="$!"
	retry "$ALMAGEST_E2E_TIMEOUT" "DoT TLS listener" nc -z 127.0.0.1 5853 \
		|| fail "DoT wrapper did not listen on 127.0.0.1:5853"
}

dns_client_status() {
	local node="$1"
	cli graph menu --name "$node" | jq -er '
		.blocks[]
		| select(.id == "result")
		| .items[]
		| select(.id == "status")
		| .text
	'
}

dns_client_field() {
	local node="$1"
	local field="$2"
	cli graph menu --name "$node" | jq -er --arg field "$field" '
		.blocks[]
		| select(.id == "result")
		| .items[]
		| select(.id == $field)
		| (.value // "")
	'
}

wait_dns_response() {
	local node="$1"
	local ip="$2"
	retry "$ALMAGEST_E2E_TIMEOUT" "$node DNS response $ip" \
		dns_response_ready "$node" "$ip"
}

dns_response_ready() {
	local node="$1"
	local ip="$2"
	local status
	local err
	local resp
	status="$(dns_client_status "$node")" || return 1
	err="$(dns_client_field "$node" error)" || return 1
	resp="$(dns_client_field "$node" response)" || return 1
	[[ "$status" == "Status: NOERROR" && -z "$err" && "$resp" == *"$ip"* ]]
}

trap cleanup EXIT

start_dnsmasq
install_dot_ca_and_start_wrapper
start_daemon
trap cleanup EXIT

wait_node_class_available "almagest/DnsClient" \
	|| fail "DnsClient class is unavailable"

for node in "OutNetwork" "Bootstrap DNS" "DNS UDP Client" "DNS DoT Client" "UDP Query" "DoT Query"; do
	wait_graph_node "$node" || fail "$node node missing"
done

udp_transport="$(node_id_by_name "DNS UDP Client")"
dot_transport="$(node_id_by_name "DNS DoT Client")"
wait_node_label "$udp_transport" "1 DNS server"
wait_node_label "$dot_transport" "1 DNS server"

cli graph click-menu-button --name "UDP Query" --block request --button send >/dev/null
wait_dns_response "UDP Query" "198.51.100.10" \
	|| fail "UDP DNS query did not return dnsmasq answer"

cli graph click-menu-button --name "DoT Query" --block request --button send >/dev/null
wait_dns_response "DoT Query" "198.51.100.20" \
	|| fail "DoT DNS query did not return dnsmasq answer"

log "case passed"
