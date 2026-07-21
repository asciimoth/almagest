#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

query_name="default-tun-dns.e2e.test"
provider_ip="192.0.2.41"
bypass_ip="192.0.2.99"
provider_pid=""
bypass_pid=""
provider_log="$E2E_RUNTIME/provider-dns.log"
bypass_log="$E2E_RUNTIME/bypass-dns.log"

cleanup() {
	if [[ -n "$provider_pid" ]]; then
		kill "$provider_pid" 2>/dev/null || true
		wait "$provider_pid" 2>/dev/null || true
	fi
	if [[ -n "$bypass_pid" ]]; then
		kill "$bypass_pid" 2>/dev/null || true
		wait "$bypass_pid" 2>/dev/null || true
	fi
	stop_daemon
	if [[ -x "$E2E_RUNTIME/cleanup-resolv.sh" ]]; then
		"$E2E_RUNTIME/cleanup-resolv.sh"
	fi
}

start_dnsmasq() {
	local port="$1"
	local ip="$2"
	local conf="$3"
	local log_file="$4"

	cat >"$conf" <<EOF
port=$port
listen-address=127.0.0.1
bind-interfaces
no-resolv
no-hosts
log-queries
log-facility=$log_file
address=/$query_name/$ip
EOF
	dnsmasq --no-daemon --conf-file="$conf" >/dev/null 2>"$log_file.stderr" &
	echo "$!"
}

resolve_query_name() {
	getent ahostsv4 "$query_name" | awk 'NR == 1 { print $1 }'
}

host_resolves_to_provider() {
	[[ "$(resolve_query_name)" == "$provider_ip" ]]
}

log_has_query() {
	local log_file="$1"
	grep -F -- "$query_name" "$log_file" >/dev/null
}

log_has_no_query() {
	local log_file="$1"
	! log_has_query "$log_file"
}

trap cleanup EXIT

provider_pid="$(start_dnsmasq 5354 "$provider_ip" "$E2E_RUNTIME/provider-dnsmasq.conf" "$provider_log")"
bypass_pid="$(start_dnsmasq 5355 "$bypass_ip" "$E2E_RUNTIME/bypass-dnsmasq.conf" "$bypass_log")"

retry "$ALMAGEST_E2E_TIMEOUT" "provider DNS listener" nc -z 127.0.0.1 5354 \
	|| fail "provider DNS did not listen on 127.0.0.1:5354"
retry "$ALMAGEST_E2E_TIMEOUT" "bypass DNS listener" nc -z 127.0.0.1 5355 \
	|| fail "bypass DNS did not listen on 127.0.0.1:5355"

start_daemon
trap cleanup EXIT

wait_node_class_available "almagest/DefaultTun" \
	|| fail "DefaultTun node class is unavailable"
wait_node_class_available "almagest/DnsClient" \
	|| fail "DnsClient node class is unavailable"
wait_node_class_available "almagest/OutNetwork" \
	|| fail "OutNetwork node class is unavailable"

wait_graph_node "DefaultTun DNS 1701" || fail "DefaultTun DNS node missing"
wait_graph_node "Provider DNS 1702" || fail "Provider DNS node missing"
wait_graph_node "OutNetwork 1703" || fail "OutNetwork node missing"

default_tun="$(node_id_by_name "DefaultTun DNS 1701")"
provider_dns="$(node_id_by_name "Provider DNS 1702")"
wait_node_label "$default_tun" ready || fail "DefaultTun did not become ready"
wait_node_label "$provider_dns" "1 DNS server" || fail "Provider DNS did not become ready"

retry "$ALMAGEST_E2E_TIMEOUT" "system resolver uses DefaultTun connected provider" \
	host_resolves_to_provider \
	|| fail "system resolver did not return provider DNS answer"

retry "$ALMAGEST_E2E_TIMEOUT" "provider DNS received system query" \
	log_has_query "$provider_log" \
	|| fail "connected provider DNS did not receive the system query"

log_has_no_query "$bypass_log" \
	|| fail "system DNS query bypassed the connected DefaultTun DNS provider"

log "case passed"
