#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

original_dns_address="127.0.0.54"
managed_dns_address="127.0.0.55"
managed_dns_port="5354"
http_port="18095"
ipv6_http_port="18096"
localhost_http_port="18097"
original_target="203.0.113.2"
hosts_target="203.0.113.3"
poison_target="203.0.113.4"
original_body="original upstream and bypass route"
hosts_body="hosts file and bypass route"
poison_body="managed resolver poison target"
ipv6_body="IPv6 hosts file target"
localhost_body="localhost target"
system_name="system-managed.e2e.test"
system_original_answer="192.0.2.40"
system_managed_answer="192.0.2.41"
dns_before_name="dns-before-takeover.e2e.test"
dns_after_name="dns-after-takeover.e2e.test"
dns_detached_name="dns-while-managed-detached.e2e.test"
dns_rebuilt_name="dns-after-default-tun-rebuild.e2e.test"
dynamic_name="hosts-dynamic.e2e.test"
missing_name="outnet-missing.e2e.test"
original_dns_pid=""
managed_dns_pid=""
target_pids=()
original_dns_log="$E2E_RUNTIME/original-dns.log"
managed_dns_log="$E2E_RUNTIME/managed-dns.log"

cleanup() {
	trap - EXIT
	stop_daemon
	for pid in "${target_pids[@]}"; do
		stop_static_http_target "$pid"
	done
	for pid in "$managed_dns_pid" "$original_dns_pid"; do
		if [[ -n "$pid" ]]; then
			kill "$pid" 2>/dev/null || true
			wait "$pid" 2>/dev/null || true
		fi
	done
	if [[ -x "$E2E_RUNTIME/cleanup-system.sh" ]]; then
		"$E2E_RUNTIME/cleanup-system.sh"
	fi
}

start_dnsmasq() {
	local config="$1"
	local stdout_log="$2"
	local stderr_log="$3"
	dnsmasq --no-daemon --conf-file="$config" >"$stdout_log" 2>"$stderr_log" &
	echo "$!"
}

dns_answers() {
	local server="$1"
	local port="$2"
	local name="$3"
	local answer="$4"
	dig +time=1 +tries=1 +short -p "$port" "@$server" "$name" A \
		| grep -Fx "$answer" >/dev/null
}

dns_query_count() {
	local log_file="$1"
	local name="$2"
	grep -Fci -- "$name" "$log_file" 2>/dev/null || true
}

assert_no_new_dns_query() {
	local log_file="$1"
	local name="$2"
	local before="$3"
	local after
	after="$(dns_query_count "$log_file" "$name")"
	[[ "$after" == "$before" ]] \
		|| fail "unexpected DNS query for $name in $log_file: $before -> $after"
}

proxy_get() {
	local name="$1"
	local port="$2"
	local path="$3"
	local output="$4"
	curl --fail --silent --show-error \
		--connect-timeout 2 --max-time 6 \
		--socks5-hostname 127.0.0.1:1081 \
		"http://$name:$port$path" --output "$output"
}

assert_proxy_body() {
	local name="$1"
	local port="$2"
	local path="$3"
	local want="$4"
	local output="$E2E_RUNTIME/body-${path//\//_}.txt"
	retry "$ALMAGEST_E2E_TIMEOUT" "OutNetwork request to $name$path" \
		proxy_get "$name" "$port" "$path" "$output" \
		|| fail "OutNetwork request to $name$path failed"
	local got
	got="$(cat "$output")"
	[[ "$got" == "$want" ]] \
		|| fail "response from $name$path = $got, want $want"
}

proxy_request_fails() {
	local name="$1"
	if proxy_get "$name" "$http_port" /must-not-connect \
		"$E2E_RUNTIME/unexpected-missing-body.txt" \
		>"$E2E_RUNTIME/unexpected-missing-stdout.txt" \
		2>"$E2E_RUNTIME/expected-missing-error.txt"; then
		return 1
	fi
	return 0
}

system_resolves_to() {
	local answer="$1"
	[[ "$(getent ahostsv4 "$system_name" | awk 'NR == 1 { print $1 }')" == "$answer" ]]
}

direct_public_request_fails() {
	if curl --fail --silent --show-error --noproxy '*' \
		--connect-timeout 1 --max-time 3 \
		"http://$original_target:$http_port/direct-must-be-captured" \
		--output "$E2E_RUNTIME/unexpected-direct-body.txt" \
		>"$E2E_RUNTIME/unexpected-direct-stdout.txt" \
		2>"$E2E_RUNTIME/expected-direct-error.txt"; then
		return 1
	fi
	return 0
}

activate_default_tun() {
	local node
	node="$(add_node "almagest/DefaultTun" "DefaultTun OutNetwork DNS")"
	wait_node_label "$node" ready || fail "DefaultTun did not become ready"
	add_link_by_port_name "$node" right DNS "$managed_dns_node" left DNS \
		>"$E2E_RUNTIME/default-tun-dns-link.json"
	wait_node_label "$managed_dns_node" "1 DNS server" \
		|| fail "managed DnsClient did not remain ready"
	printf '%s\n' "$node"
}

remove_dynamic_hosts_entry() {
	awk -v name="$dynamic_name" 'index($0, name) == 0' /etc/hosts \
		>"$E2E_RUNTIME/hosts-without-dynamic"
	cp "$E2E_RUNTIME/hosts-without-dynamic" /etc/hosts
}

run_concurrent_requests() {
	local pids=()
	local index
	for index in 1 2 3 4; do
		proxy_get "$dns_after_name" "$http_port" "/concurrent-dns-$index" \
			"$E2E_RUNTIME/concurrent-dns-$index.txt" &
		pids+=("$!")
		proxy_get "hosts-alias.e2e.test" "$http_port" "/concurrent-hosts-$index" \
			"$E2E_RUNTIME/concurrent-hosts-$index.txt" &
		pids+=("$!")
	done
	for index in "${!pids[@]}"; do
		wait "${pids[$index]}" \
			|| fail "concurrent OutNetwork request $index failed"
	done
	for index in 1 2 3 4; do
		[[ "$(cat "$E2E_RUNTIME/concurrent-dns-$index.txt")" == "$original_body" ]] \
			|| fail "concurrent DNS request $index used the wrong target"
		[[ "$(cat "$E2E_RUNTIME/concurrent-hosts-$index.txt")" == "$hosts_body" ]] \
			|| fail "concurrent hosts request $index used the wrong target"
	done
}

trap cleanup EXIT

cat >"$E2E_RUNTIME/original-dnsmasq.conf" <<EOF
port=53
listen-address=$original_dns_address
bind-interfaces
no-resolv
no-hosts
cache-size=0
log-queries
log-facility=$original_dns_log
host-record=$system_name,$system_original_answer
host-record=$dns_before_name,$original_target
host-record=$dns_after_name,$original_target
host-record=$dns_detached_name,$original_target
host-record=$dns_rebuilt_name,$original_target
host-record=$dynamic_name,$original_target
host-record=hosts-precedence.e2e.test,$poison_target
address=/$missing_name/#
EOF
cat >"$E2E_RUNTIME/managed-dnsmasq.conf" <<EOF
port=$managed_dns_port
listen-address=$managed_dns_address
bind-interfaces
no-resolv
no-hosts
cache-size=0
log-queries
log-facility=$managed_dns_log
host-record=$system_name,$system_managed_answer
address=/#/$poison_target
EOF

original_dns_pid="$(start_dnsmasq \
	"$E2E_RUNTIME/original-dnsmasq.conf" \
	"$E2E_RUNTIME/original-dns.stdout" \
	"$E2E_RUNTIME/original-dns.stderr")"
managed_dns_pid="$(start_dnsmasq \
	"$E2E_RUNTIME/managed-dnsmasq.conf" \
	"$E2E_RUNTIME/managed-dns.stdout" \
	"$E2E_RUNTIME/managed-dns.stderr")"

retry "$ALMAGEST_E2E_TIMEOUT" "original DNS answer" \
	dns_answers "$original_dns_address" 53 "$dns_before_name" "$original_target" \
	|| fail "original DNS server did not become ready"
retry "$ALMAGEST_E2E_TIMEOUT" "managed DNS answer" \
	dns_answers "$managed_dns_address" "$managed_dns_port" "$system_name" "$system_managed_answer" \
	|| fail "managed DNS server did not become ready"

target_pids+=("$(start_static_http_target_in_netns \
	alm-dns-public "$original_target" "$http_port" "$original_body" \
	"$E2E_RUNTIME/original-target.log")")
target_pids+=("$(start_static_http_target_in_netns \
	alm-dns-public "$hosts_target" "$http_port" "$hosts_body" \
	"$E2E_RUNTIME/hosts-target.log")")
target_pids+=("$(start_static_http_target_in_netns \
	alm-dns-public "$poison_target" "$http_port" "$poison_body" \
	"$E2E_RUNTIME/poison-target.log")")
target_pids+=("$(start_static_http_target \
	::1 "$ipv6_http_port" "$ipv6_body" "$E2E_RUNTIME/ipv6-target.log")")
target_pids+=("$(start_static_http_target \
	127.0.0.1 "$localhost_http_port" "$localhost_body" \
	"$E2E_RUNTIME/localhost-target.log")")

start_daemon
trap cleanup EXIT

wait_node_class_available "almagest/DefaultTun" \
	|| fail "DefaultTun node class is unavailable"
wait_node_class_available "almagest/LocalProxy" \
	|| fail "LocalProxy node class is unavailable"
wait_graph_node "Managed DNS 1702" || fail "managed DnsClient node missing"
wait_graph_node "OutNetwork Proxy 1704" || fail "OutNetwork proxy node missing"
managed_dns_node="$(node_id_by_name "Managed DNS 1702")"
wait_node_label "$managed_dns_node" "1 DNS server" \
	|| fail "managed DnsClient did not become ready"
wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:1081" \
	|| fail "OutNetwork test proxy did not start"

# Before DNS takeover, both the system resolver and OutNetwork use the
# original upstream. Local data still has priority.
retry "$ALMAGEST_E2E_TIMEOUT" "system resolver before DefaultTun" \
	system_resolves_to "$system_original_answer" \
	|| fail "system resolver did not use the original DNS before DefaultTun"
assert_proxy_body "$dns_before_name" "$http_port" /before-default-tun "$original_body"

original_static_before="$(dns_query_count "$original_dns_log" hosts-static.e2e.test)"
managed_static_before="$(dns_query_count "$managed_dns_log" hosts-static.e2e.test)"
assert_proxy_body hosts-static.e2e.test "$http_port" /hosts-static-before "$hosts_body"
assert_proxy_body HOSTS-ALIAS.E2E.TEST. "$http_port" /hosts-alias-before "$hosts_body"
assert_proxy_body hosts-case.e2e.test "$http_port" /hosts-case-before "$hosts_body"
assert_no_new_dns_query "$original_dns_log" hosts-static.e2e.test "$original_static_before"
assert_no_new_dns_query "$managed_dns_log" hosts-static.e2e.test "$managed_static_before"

precedence_original_before="$(dns_query_count "$original_dns_log" hosts-precedence.e2e.test)"
assert_proxy_body hosts-precedence.e2e.test "$http_port" /hosts-priority-before "$hosts_body"
assert_no_new_dns_query "$original_dns_log" hosts-precedence.e2e.test "$precedence_original_before"

assert_proxy_body LOCALHOST. "$localhost_http_port" /localhost-before "$localhost_body"
assert_proxy_body hosts-ipv6.e2e.test "$ipv6_http_port" /hosts-ipv6-before "$ipv6_body"
literal_queries_before="$(dns_query_count "$original_dns_log" "$original_target")"
assert_proxy_body "$original_target" "$http_port" /literal-before "$original_body"
assert_no_new_dns_query "$original_dns_log" "$original_target" "$literal_queries_before"

default_tun="$(activate_default_tun)"
retry "$ALMAGEST_E2E_TIMEOUT" "system resolver after DefaultTun takeover" \
	system_resolves_to "$system_managed_answer" \
	|| fail "system resolver did not use the managed DNS after DefaultTun"
retry "$ALMAGEST_E2E_TIMEOUT" "managed DNS received the system query" \
	grep -Fi "$system_name" "$managed_dns_log" >/dev/null \
	|| fail "managed DNS did not receive the system resolver query"

retry "$ALMAGEST_E2E_TIMEOUT" "direct public route captured by DefaultTun" \
	direct_public_request_fails \
	|| fail "direct public traffic bypassed DefaultTun"

# OutNetwork must use the saved original upstream and its marked route. The
# managed DNS has a poison answer for this name.
managed_after_before="$(dns_query_count "$managed_dns_log" "$dns_after_name")"
assert_proxy_body "$dns_after_name" "$http_port" /after-default-tun "$original_body"
retry "$ALMAGEST_E2E_TIMEOUT" "original DNS query after takeover" \
	grep -Fi "$dns_after_name" "$original_dns_log" >/dev/null \
	|| fail "OutNetwork did not query the original DNS after takeover"
assert_no_new_dns_query "$managed_dns_log" "$dns_after_name" "$managed_after_before"

# Hosts data must win over both DNS servers after takeover.
for name in hosts-static.e2e.test HOSTS-ALIAS.E2E.TEST. hosts-case.e2e.test; do
	normalized_name="${name,,}"
	normalized_name="${normalized_name%.}"
	original_before="$(dns_query_count "$original_dns_log" "$normalized_name")"
	managed_before="$(dns_query_count "$managed_dns_log" "$normalized_name")"
	assert_proxy_body "$name" "$http_port" "/after-${name//./-}" "$hosts_body"
	assert_no_new_dns_query "$original_dns_log" "$normalized_name" "$original_before"
	assert_no_new_dns_query "$managed_dns_log" "$normalized_name" "$managed_before"
done
original_before="$(dns_query_count "$original_dns_log" hosts-precedence.e2e.test)"
managed_before="$(dns_query_count "$managed_dns_log" hosts-precedence.e2e.test)"
assert_proxy_body hosts-precedence.e2e.test "$http_port" /hosts-priority-after "$hosts_body"
assert_no_new_dns_query "$original_dns_log" hosts-precedence.e2e.test "$original_before"
assert_no_new_dns_query "$managed_dns_log" hosts-precedence.e2e.test "$managed_before"

assert_proxy_body LOCALHOST. "$localhost_http_port" /localhost-after "$localhost_body"
assert_proxy_body hosts-ipv6.e2e.test "$ipv6_http_port" /hosts-ipv6-after "$ipv6_body"

# A provider failure must not fall back to the managed system resolver. The
# managed wildcard would connect to the poison target if fallback occurred.
managed_missing_before="$(dns_query_count "$managed_dns_log" "$missing_name")"
retry "$ALMAGEST_E2E_TIMEOUT" "OutNetwork NXDOMAIN failure" \
	proxy_request_fails "$missing_name" \
	|| fail "OutNetwork connected after the original DNS returned NXDOMAIN"
retry "$ALMAGEST_E2E_TIMEOUT" "original DNS NXDOMAIN query" \
	grep -Fi "$missing_name" "$original_dns_log" >/dev/null \
	|| fail "original DNS did not receive the missing-name query"
assert_no_new_dns_query "$managed_dns_log" "$missing_name" "$managed_missing_before"

# The resolver must read /etc/hosts for each new operation.
assert_proxy_body "$dynamic_name" "$http_port" /dynamic-dns-before "$original_body"
dynamic_original_before="$(dns_query_count "$original_dns_log" "$dynamic_name")"
printf '%s %s\n' "$hosts_target" "$dynamic_name" >>/etc/hosts
assert_proxy_body "$dynamic_name" "$http_port" /dynamic-hosts "$hosts_body"
assert_no_new_dns_query "$original_dns_log" "$dynamic_name" "$dynamic_original_before"
remove_dynamic_hosts_entry
assert_proxy_body "$dynamic_name" "$http_port" /dynamic-dns-after "$original_body"
dynamic_original_after="$(dns_query_count "$original_dns_log" "$dynamic_name")"
((dynamic_original_after > dynamic_original_before)) \
	|| fail "OutNetwork did not return to DNS after the hosts entry was removed"

# Local and DNS-backed requests must stay correct under concurrent use.
managed_concurrent_before="$(dns_query_count "$managed_dns_log" "$dns_after_name")"
run_concurrent_requests
assert_no_new_dns_query "$managed_dns_log" "$dns_after_name" "$managed_concurrent_before"

# Removing the managed DNS link must not affect OutNetwork resolution.
dns_link="$(link_id_by_port_names \
	"DefaultTun OutNetwork DNS" DNS "Managed DNS 1702" DNS)"
cli graph delete-link --id "$dns_link" >"$E2E_RUNTIME/delete-managed-dns-link.json"
managed_detached_before="$(dns_query_count "$managed_dns_log" "$dns_detached_name")"
assert_proxy_body "$dns_detached_name" "$http_port" /managed-dns-detached "$original_body"
assert_no_new_dns_query "$managed_dns_log" "$dns_detached_name" "$managed_detached_before"
add_link_by_port_name "$default_tun" right DNS "$managed_dns_node" left DNS \
	>"$E2E_RUNTIME/reattach-managed-dns-link.json"
retry "$ALMAGEST_E2E_TIMEOUT" "system resolver after DNS link reattach" \
	system_resolves_to "$system_managed_answer" \
	|| fail "system resolver did not recover after the DNS link was reattached"

# Rebuilding DefaultTun must preserve the saved original resolver path.
cli graph delete-node --id "$default_tun" >"$E2E_RUNTIME/delete-default-tun.json"
retry "$ALMAGEST_E2E_TIMEOUT" "system resolver after DefaultTun removal" \
	system_resolves_to "$system_original_answer" \
	|| fail "system resolver did not restore the original DNS"
default_tun="$(activate_default_tun)"
retry "$ALMAGEST_E2E_TIMEOUT" "system resolver after DefaultTun rebuild" \
	system_resolves_to "$system_managed_answer" \
	|| fail "system resolver did not use managed DNS after DefaultTun rebuild"
managed_rebuilt_before="$(dns_query_count "$managed_dns_log" "$dns_rebuilt_name")"
assert_proxy_body "$dns_rebuilt_name" "$http_port" /after-default-tun-rebuild "$original_body"
assert_no_new_dns_query "$managed_dns_log" "$dns_rebuilt_name" "$managed_rebuilt_before"

wait_log_contains "almagest/LogNetwork" "Dial tcp $dns_after_name:$http_port" \
	|| fail "LogNetwork did not observe the host-name OutNetwork dial"
grep -F "GET /after-default-tun" "$E2E_RUNTIME/original-target.log" >/dev/null \
	|| fail "original target did not receive the post-takeover request"
if grep -F "GET /" "$E2E_RUNTIME/poison-target.log" >/dev/null 2>&1; then
	fail "the managed resolver poison target received an OutNetwork request"
fi

log "case passed"
