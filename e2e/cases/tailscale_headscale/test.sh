#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

headscale_pid=""
# These variables are read through indirect expansion in stop_processes.
# shellcheck disable=SC2034
daemon_pid_a=""
# shellcheck disable=SC2034
daemon_pid_b=""
admin_sock_a="$E2E_RUNTIME/daemon-a.sock"
admin_sock_b="$E2E_RUNTIME/daemon-b.sock"
daemon_log_a="$E2E_RUNTIME/daemon-a.log"
daemon_log_b="$E2E_RUNTIME/daemon-b.log"
headscale_log="$E2E_RUNTIME/headscale.log"

fail() {
	log "FAILED: $*"
	if [[ -n "$headscale_pid" ]] && kill -0 "$headscale_pid" 2>/dev/null; then
		log "Headscale nodes follow"
		headscale --config "$E2E_CONFIG_DIR/headscale.yaml" nodes list --output json >&2 || true
	fi
	if [[ -S "$admin_sock_a" ]]; then
		log "Tailscale A menu follows"
		cli_a graph menu --name "Tailscale A" >&2 || true
		log "WireGuard A menu follows"
		cli_a graph menu --name "WireGuard A" >&2 || true
	fi
	if [[ -S "$admin_sock_b" ]]; then
		log "Tailscale B menu follows"
		cli_b graph menu --name "Tailscale B" >&2 || true
		log "WireGuard B menu follows"
		cli_b graph menu --name "WireGuard B" >&2 || true
	fi
	for output in "$headscale_log" "$daemon_log_a" "$daemon_log_b"; do
		if [[ -f "$output" ]]; then
			log "$(basename "$output") follows"
			tail -200 "$output" >&2 || true
		fi
	done
	exit 1
}

cli_a() { almagest-cli --url "unix://$admin_sock_a" "$@"; }
cli_b() { almagest-cli --url "unix://$admin_sock_b" "$@"; }

stop_processes() {
	trap - EXIT
	for pid_var in daemon_pid_a daemon_pid_b headscale_pid; do
		local pid="${!pid_var:-}"
		if [[ -z "$pid" ]]; then
			continue
		fi
		if kill -0 "$pid" 2>/dev/null; then
			kill "$pid" 2>/dev/null || true
			local deadline=$((SECONDS + 10))
			while kill -0 "$pid" 2>/dev/null && ((SECONDS <= deadline)); do
				sleep 0.25
			done
			if kill -0 "$pid" 2>/dev/null; then
				kill -KILL "$pid" 2>/dev/null || true
			fi
		fi
		wait "$pid" 2>/dev/null || true
	done
}
trap stop_processes EXIT

start_headscale() {
	local config="$E2E_CONFIG_DIR/headscale.yaml"
	: >"$headscale_log"
	headscale serve --config "$config" >"$headscale_log" 2>&1 &
	headscale_pid="$!"
	retry "$ALMAGEST_E2E_TIMEOUT" "Headscale listener" nc -z 127.0.0.1 18090 \
		|| fail "Headscale did not listen"
	headscale --config "$config" users create e2e >/dev/null
	local auth_key
	auth_key="$(headscale --config "$config" preauthkeys create --user 1 --reusable --expiration 2h)"
	[[ -n "$auth_key" ]] || fail "Headscale did not create an auth key"
	for protected in "$E2E_CONFIG_DIR"/daemon-*/protected_alm.hujson; do
		sed -i "s@%HEADSCALE_AUTH_KEY%@$auth_key@g" "$protected"
	done
}

start_test_daemon() {
	local suffix="$1"
	local config="$E2E_CONFIG_DIR/daemon-$suffix"
	local socket_var="admin_sock_$suffix"
	local log_var="daemon_log_$suffix"
	local pid_var="daemon_pid_$suffix"
	local socket="${!socket_var}"
	local output="${!log_var}"
	: >"$output"
	XDG_RUNTIME_DIR="$E2E_RUNTIME" almagestd \
		-c "$config" \
		-l debug \
		--debug-nodes \
		--unsafe-config \
		--tmp-file-storage \
		--unix "$socket" \
		--web 0 \
		>"$output" 2>&1 &
	printf -v "$pid_var" '%s' "$!"
	retry "$ALMAGEST_E2E_TIMEOUT" "daemon $suffix admin socket" test -S "$socket" \
		|| fail "daemon $suffix admin socket was not created"
}

node_id() {
	local daemon="$1"
	local name="$2"
	"cli_$daemon" graph map | jq -er --arg name "$name" '.nodes[$name].ID'
}

node_label_is_for() {
	local daemon="$1"
	local node="$2"
	local label="$3"
	[[ "$("cli_$daemon" graph node --id "$node" | jq -er '.snapshot.label')" == "$label" ]]
}

wait_node_label_for() {
	local daemon="$1"
	local node="$2"
	local label="$3"
	retry 60 "daemon $daemon node $node label $label" node_label_is_for "$daemon" "$node" "$label"
}

menu_field() {
	local daemon="$1"
	local node_name="$2"
	local block="$3"
	local field="$4"
	"cli_$daemon" graph menu --name "$node_name" | jq -er --arg block "$block" --arg field "$field" '
		.blocks[] | select(.id == $block) | .items[] | select(.id == $field) | (.value // .text // "")
	'
}

tailscale_peer_is_direct() {
	local value
	value="$(menu_field "$1" "$2" status peers)" || return 1
	[[ "$value" == *"endpoints="* && "$value" != *"endpoints= |"* && "$value" == *"path=direct-udp"* ]]
}

tailscale_acl_is_present() {
	local value
	value="$(menu_field "$1" "$2" status acl)" || return 1
	jq -e '.rules | length > 0' <<<"$value" >/dev/null
}

firewall_policy_is_present() {
	local value
	value="$(menu_field b "Tailnet Firewall B" effective config)" || return 1
	jq -e '.include | length > 0' <<<"$value" >/dev/null
}

dns_response_ready() {
	local expected="$1"
	local status error response
	status="$(menu_field a "MagicDNS Query" result status)" || return 1
	error="$(menu_field a "MagicDNS Query" result error)" || return 1
	response="$(menu_field a "MagicDNS Query" result response)" || return 1
	[[ "$status" == "Status: NOERROR" && -z "$error" && "$response" == *"$expected"* ]]
}

set_dns_query_field() {
	local node="$1"
	local field="$2"
	local value="$3"
	local message
	message="$(jq -nc \
		--arg menu "NODE${node}MENU" \
		--arg field "$field" \
		--arg value "$value" \
		'{
			type:"field.update",
			menuId:$menu,
			field:{blockId:"request",fieldId:$field},
			value:$value
		}')"
	cli_a engine send-node-formular-msg --id "$node" --message "$message" >/dev/null
}

query_magic_dns() {
	local node="$1"
	local name="$2"
	local type="$3"
	local expected="$4"
	set_dns_query_field "$node" name "$name"
	set_dns_query_field "$node" type "$type"
	cli_a graph click-menu-button --id "$node" --block request --button send >/dev/null
	retry 30 "MagicDNS $name $type response" dns_response_ready "$expected" \
		|| fail "Tailscale DNS port did not resolve $name $type"
}

http_response_ready() {
	local status error response
	status="$(menu_field a "HTTP Client A" result status)" || return 1
	error="$(menu_field a "HTTP Client A" result error)" || return 1
	response="$(menu_field a "HTTP Client A" result response)" || return 1
	[[ "$status" == "Status: 200 OK" && -z "$error" && "$response" == "hello from tailscale beta" ]]
}

start_headscale
start_test_daemon a

tailscale_a="$(node_id a "Tailscale A")"
wireguard_a="$(node_id a "WireGuard A")"
wait_node_label_for a "$tailscale_a" up || fail "Tailscale A did not become ready"
wait_node_label_for a "$wireguard_a" up || fail "WireGuard A did not become ready"

# Starting A first makes the control-map changes deterministic and also checks
# that a client can reach running state before any peers exist.
start_test_daemon b
tailscale_b="$(node_id b "Tailscale B")"
wireguard_b="$(node_id b "WireGuard B")"
wait_node_label_for b "$tailscale_b" up || fail "Tailscale B did not become ready"
wait_node_label_for b "$wireguard_b" up || fail "WireGuard B did not become ready"

retry 60 "Tailscale A direct peer path" tailscale_peer_is_direct a "Tailscale A" \
	|| fail "Tailscale A did not select direct UDP"
retry 60 "Tailscale B direct peer path" tailscale_peer_is_direct b "Tailscale B" \
	|| fail "Tailscale B did not select direct UDP"
retry 30 "Tailscale ACL in node menu" tailscale_acl_is_present a "Tailscale A" \
	|| fail "Tailscale A menu did not expose ACL rules"
retry 30 "Tailscale firewall object" firewall_policy_is_present \
	|| fail "NetworkFirewall did not receive the Tailscale policy object"

magic_dns_query="$(node_id a "MagicDNS Query")"
query_magic_dns "$magic_dns_query" beta.e2e.internal A "100.64."
query_magic_dns "$magic_dns_query" v4.magic.e2e.internal A "192.0.2.10"
query_magic_dns "$magic_dns_query" v4-alt.magic.e2e.internal A "192.0.2.10"
query_magic_dns "$magic_dns_query" v6.magic.e2e.internal AAAA "2001:db8::10"
query_magic_dns "$magic_dns_query" v6-alt.magic.e2e.internal AAAA "2001:db8::10"
query_magic_dns "$magic_dns_query" 10.2.0.192.in-addr.arpa PTR "v4.magic.e2e.internal."
query_magic_dns "$magic_dns_query" 10.2.0.192.in-addr.arpa PTR "v4-alt.magic.e2e.internal."
ipv6_ptr="0.1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa"
query_magic_dns "$magic_dns_query" "$ipv6_ptr" PTR "v6.magic.e2e.internal."
query_magic_dns "$magic_dns_query" "$ipv6_ptr" PTR "v6-alt.magic.e2e.internal."
query_magic_dns "$magic_dns_query" alias.magic.e2e.internal CNAME "v4.magic.e2e.internal."
query_magic_dns "$magic_dns_query" zone.magic.e2e.internal NS "ns1.magic.e2e.internal."
query_magic_dns "$magic_dns_query" mail.magic.e2e.internal MX "10 mx1.magic.e2e.internal."
query_magic_dns "$magic_dns_query" _service._tcp.magic.e2e.internal SRV \
	"10 20 8443 target.magic.e2e.internal."
query_magic_dns "$magic_dns_query" text.magic.e2e.internal TXT "tailscale magic text"

cli_a graph click-menu-button --name "HTTP Client A" --block request --button send >/dev/null
retry 60 "HTTP over Tailscale direct path" http_response_ready \
	|| fail "daemon A did not reach daemon B through Tailscale"

log "case passed"
