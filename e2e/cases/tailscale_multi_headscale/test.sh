#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

ALMAGEST_E2E_TIMEOUT=60
ALMAGEST_E2E_WEB_ADMIN=18080
ALMAGEST_E2E_WEB_PPROF=1
# These variables are read and written by name for each Headscale instance.
# shellcheck disable=SC2034
headscale_pid_a=""
# shellcheck disable=SC2034
headscale_pid_b=""
headscale_log_a="$E2E_RUNTIME/headscale-a.log"
headscale_log_b="$E2E_RUNTIME/headscale-b.log"

headscale_config() {
	printf '%s/headscale-%s.yaml' "$E2E_CONFIG_DIR" "$1"
}

headscale_cli() {
	local server="$1"
	shift
	headscale --config "$(headscale_config "$server")" "$@"
}

fail() {
	log "FAILED: $*"
	for server in a b; do
		local pid_var="headscale_pid_$server"
		local pid="${!pid_var:-}"
		if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
			log "Headscale $server nodes follow"
			headscale_cli "$server" nodes list --output json >&2 || true
		fi
	done
	if [[ -S "$E2E_ADMIN_SOCK" ]]; then
		for node in "Tailscale A1" "Tailscale A2" "Tailscale B1" "Tailscale B2"; do
			log "$node menu follows"
			cli graph menu --name "$node" >&2 || true
		done
	fi
	for output in "$headscale_log_a" "$headscale_log_b" "$E2E_DAEMON_LOG"; do
		if [[ -f "$output" ]]; then
			log "$(basename "$output") follows"
			tail -200 "$output" >&2 || true
		fi
	done
	exit 1
}

stop_headscales() {
	for pid_var in headscale_pid_a headscale_pid_b; do
		local pid="${!pid_var:-}"
		if [[ -z "$pid" ]]; then
			continue
		fi
		if kill -0 "$pid" 2>/dev/null; then
			kill "$pid" 2>/dev/null || true
		fi
		wait "$pid" 2>/dev/null || true
		printf -v "$pid_var" '%s' ""
	done
}

stop_all() {
	trap - EXIT
	stop_daemon
	stop_headscales
}

start_headscale() {
	local server="$1"
	local port="$2"
	local output_var="headscale_log_$server"
	local output="${!output_var}"
	local pid_var="headscale_pid_$server"
	: >"$output"
	headscale serve --config "$(headscale_config "$server")" >"$output" 2>&1 &
	printf -v "$pid_var" '%s' "$!"
	retry "$ALMAGEST_E2E_TIMEOUT" "Headscale $server listener" nc -z 127.0.0.1 "$port" \
		|| fail "Headscale $server did not listen"
	headscale_cli "$server" users create e2e >/dev/null
	local auth_key
	auth_key="$(headscale_cli "$server" preauthkeys create --user 1 --reusable --expiration 2h)"
	[[ -n "$auth_key" ]] || fail "Headscale $server did not create an auth key"
	local marker="%HEADSCALE_${server^^}_AUTH_KEY%"
	sed -i "s@$marker@$auth_key@g" "$E2E_CONFIG_DIR/protected_alm.hujson"
}

menu_field() {
	local node_name="$1"
	local block="$2"
	local field="$3"
	cli graph menu --name "$node_name" | jq -er --arg block "$block" --arg field "$field" '
		.blocks[] | select(.id == $block) | .items[] | select(.id == $field) | (.value // .text // "")
	'
}

headscale_has_two_nodes() {
	headscale_cli "$1" nodes list --output json | jq -e 'length == 2' >/dev/null
}

tailscale_peer_is_isolated_and_direct() {
	local node="$1"
	local wanted="$2"
	local forbidden="$3"
	local value
	value="$(menu_field "$node" status peers)" || return 1
	[[ "$value" == *"$wanted"* && "$value" == *"path=direct-udp"* && "$value" != *"$forbidden"* ]]
}

tailscale_control_is() {
	[[ "$(menu_field "$1" status controlURL)" == "$2" ]]
}

four_tailscale_cache_namespaces_exist() {
	cli kv entries --path nodes/tailscale | jq -e '
		.exists and ([.entries[] | select(.kind == "bucket")] | length == 4)
	' >/dev/null
}

dns_response_ready() {
	local node="$1"
	local prefix="$2"
	local status error response
	status="$(menu_field "$node" result status)" || return 1
	error="$(menu_field "$node" result error)" || return 1
	response="$(menu_field "$node" result response)" || return 1
	[[ "$status" == "Status: NOERROR" && -z "$error" && "$response" == *"$prefix"* ]]
}

http_response_ready() {
	local node="$1"
	local body="$2"
	local status error response
	status="$(menu_field "$node" result status)" || return 1
	error="$(menu_field "$node" result error)" || return 1
	response="$(menu_field "$node" result response)" || return 1
	[[ "$status" == "Status: 200 OK" && -z "$error" && "$response" == "$body" ]]
}

daemon_goroutine_count() {
	local header
	header="$(
		curl --max-time 5 -fsS \
			"http://127.0.0.1:$ALMAGEST_E2E_WEB_ADMIN/debug/pprof/goroutine?debug=1" \
			| sed -n '1p'
	)" || return 1
	[[ "$header" =~ ^goroutine\ profile:\ total\ ([0-9]+)$ ]] || return 1
	printf '%s\n' "${BASH_REMATCH[1]}"
}

daemon_cpu_ticks() {
	awk '{ print $14 + $15 }' "/proc/$daemon_pid/stat"
}

# The wgo-tailscale v0.2.2 regression created one peer-reconcile goroutine for
# each Disco latency update. Two directly connected clients could exceed
# 39,000 goroutines in seconds and continuously use several CPU cores. Keep the
# four-client graph quiet for two probe intervals and check both symptoms.
tailscale_runtime_resources_are_bounded() {
	local before_goroutines after_goroutines growth
	local before_ticks after_ticks used_ticks clock_ticks max_ticks
	before_goroutines="$(daemon_goroutine_count)" || return 1
	before_ticks="$(daemon_cpu_ticks)" || return 1
	if ((before_goroutines > 2048)); then
		log "initial daemon goroutines are already excessive: $before_goroutines"
		return 1
	fi

	sleep 10

	after_goroutines="$(daemon_goroutine_count)" || return 1
	after_ticks="$(daemon_cpu_ticks)" || return 1
	clock_ticks="$(getconf CLK_TCK)" || return 1
	growth=$((after_goroutines - before_goroutines))
	used_ticks=$((after_ticks - before_ticks))
	max_ticks=$((clock_ticks * 5))
	log "steady Tailscale resources: goroutines=$before_goroutines->$after_goroutines cpu_ticks=$used_ticks/$max_ticks"

	((after_goroutines <= 2048 && growth <= 512 && used_ticks <= max_ticks))
}

trap stop_all EXIT
start_headscale a 18190
start_headscale b 18290
start_daemon
trap stop_all EXIT

for node in \
	"Tailscale A1" "WireGuard A1" \
	"Tailscale A2" "WireGuard A2" \
	"Tailscale B1" "WireGuard B1" \
	"Tailscale B2" "WireGuard B2"; do
	id="$(node_id_by_name "$node")"
	retry 90 "$node ready" node_label_is "$id" up || fail "$node did not become ready"
done

retry 30 "two nodes on Headscale A" headscale_has_two_nodes a \
	|| fail "Headscale A did not register exactly two nodes"
retry 30 "two nodes on Headscale B" headscale_has_two_nodes b \
	|| fail "Headscale B did not register exactly two nodes"

retry 30 "Tailscale A1 control URL" tailscale_control_is "Tailscale A1" \
	"https://headscale-a.e2e.internal:18190" || fail "Tailscale A1 uses the wrong control server"
retry 30 "Tailscale B1 control URL" tailscale_control_is "Tailscale B1" \
	"https://headscale-b.e2e.internal:18290" || fail "Tailscale B1 uses the wrong control server"

retry 60 "Tailscale A1 isolated direct peer" tailscale_peer_is_isolated_and_direct \
	"Tailscale A1" "a-two.a.e2e.internal" "b-two.b.e2e.internal" \
	|| fail "Tailscale A1 did not select its isolated direct peer"
retry 60 "Tailscale A2 isolated direct peer" tailscale_peer_is_isolated_and_direct \
	"Tailscale A2" "a-one.a.e2e.internal" "b-one.b.e2e.internal" \
	|| fail "Tailscale A2 did not select its isolated direct peer"
retry 60 "Tailscale B1 isolated direct peer" tailscale_peer_is_isolated_and_direct \
	"Tailscale B1" "b-two.b.e2e.internal" "a-two.a.e2e.internal" \
	|| fail "Tailscale B1 did not select its isolated direct peer"
retry 60 "Tailscale B2 isolated direct peer" tailscale_peer_is_isolated_and_direct \
	"Tailscale B2" "b-one.b.e2e.internal" "a-one.a.e2e.internal" \
	|| fail "Tailscale B2 did not select its isolated direct peer"

retry 30 "four Tailscale cache namespaces" four_tailscale_cache_namespaces_exist \
	|| fail "Tailscale controllers did not create four independent cache namespaces"

cli graph click-menu-button --name "MagicDNS Query A" --block request --button send >/dev/null
cli graph click-menu-button --name "MagicDNS Query B" --block request --button send >/dev/null
retry 30 "MagicDNS response from tailnet A" dns_response_ready "MagicDNS Query A" "100.64.0." \
	|| fail "tailnet A MagicDNS did not resolve its peer"
retry 30 "MagicDNS response from tailnet B" dns_response_ready "MagicDNS Query B" "100.65.0." \
	|| fail "tailnet B MagicDNS did not resolve its peer"

cli graph click-menu-button --name "HTTP Client A" --block request --button send >/dev/null
cli graph click-menu-button --name "HTTP Client B" --block request --button send >/dev/null
retry 60 "HTTP traffic in tailnet A" http_response_ready "HTTP Client A" \
	"hello from isolated tailnet A" || fail "tailnet A HTTP traffic failed"
retry 60 "HTTP traffic in tailnet B" http_response_ready "HTTP Client B" \
	"hello from isolated tailnet B" || fail "tailnet B HTTP traffic failed"

tailscale_runtime_resources_are_bounded \
	|| fail "Tailscale clients did not reach a bounded steady state"

log "case passed"
