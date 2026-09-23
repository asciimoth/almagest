#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

target_body="curl to Almagest server secret"
target_log="$E2E_RUNTIME/server-secret-target.log"
target_pid=""
curl_error="$E2E_RUNTIME/server-secret-curl.err"

cleanup() {
	stop_static_http_target "$target_pid"
	stop_daemon
}

socks_request() {
	local port="$1"
	local password="$2"
	local path="$3"
	local output="$E2E_RUNTIME/server-secret-${path}.body"

	curl --fail --silent --show-error \
		--connect-timeout 1 --max-time 3 \
		--socks5-hostname "127.0.0.1:$port" \
		--proxy-user "alice:$password" \
		"http://127.0.0.1:18097/$path" \
		--output "$output" \
		2>"$curl_error" \
		&& [[ "$(<"$output")" == "$target_body" ]]
}

socks_request_fails() {
	! socks_request "$@"
}

assert_socks_success() {
	local port="$1"
	local password="$2"
	local path="$3"
	local description="$4"

	retry "$ALMAGEST_E2E_TIMEOUT" "$description" \
		socks_request "$port" "$password" "$path" \
		|| fail "$description did not succeed"
}

assert_socks_failure() {
	local port="$1"
	local password="$2"
	local path="$3"
	local description="$4"

	retry "$ALMAGEST_E2E_TIMEOUT" "$description" \
		socks_request_fails "$port" "$password" "$path" \
		|| fail "$description unexpectedly succeeded"
}

apply_socks_config() {
	cli graph apply-menu --name "SOCKS Server Secret" --json "$1" >/dev/null
}

apply_socks_user() {
	local inline_password="$1"
	local secret_name="$2"
	local connect_enabled="${3:-true}"
	local values

	values="$(jq -nc \
		--arg inline "$inline_password" \
		--arg secret "$secret_name" \
		--argjson connect "$connect_enabled" \
		'{config:{
			users:[{
				id:"alice",
				template:"user",
				values:{
					username:"alice",
					password:$inline,
					passwordSecret:$secret
				}
			}],
			cmdConnect:$connect
		}}')"
	apply_socks_config "$values"
}

menu_secret_is() {
	local expected="$1"
	cli graph menu --name "SOCKS Server Secret" | jq -e --arg expected "$expected" '
		.blocks[]
		| select(.id == "config")
		| .items[]
		| select(.id == "users")
		| .elements[]
		| select(.id == "alice")
		| .items[]
		| select(.id == "passwordSecret")
		| .value == $expected
	' >/dev/null
}

menu_has_config_error() {
	cli graph menu --name "SOCKS Server Secret" | jq -e '
		.blocks[]
		| select(.id == "config")
		| .items[]
		| select(.id == "socksPort")
		| .status == "error"
		and (.statusText | contains("must not use the same host and port"))
	' >/dev/null
}

target_pid="$(start_static_http_target 127.0.0.1 18097 "$target_body" "$target_log")"
start_daemon
trap cleanup EXIT

wait_graph_node "SOCKS Server Secret" || fail "SOCKS server node missing"
server="$(node_id_by_name "SOCKS Server Secret")"
listen_network="$(node_id_by_name "OutNetwork SOCKS Listen")"
proxy_network="$(node_id_by_name "OutNetwork SOCKS Proxy")"

wait_log_contains "almagest/SocksServer" "socks listening on 127.0.0.1:11097" \
	|| fail "SOCKS server did not start listening"

# The protected secret must override the incorrect inline fallback password.
assert_socks_failure 11097 wrong-inline-password protected-inline-rejected \
	"request with the overwritten inline password"
assert_socks_success 11097 curl-socks-secret protected-secret \
	"request with the protected secret password"

# Select one mutable secret through the Formular array field. Subsequent
# mutations exercise daemon-to-node inbox delivery and runtime replacement.
cli secrets set --key runtime-socks-password --value runtime-password-one >/dev/null
cli secrets set-nodes --key runtime-socks-password \
	--node "SOCKS Server Secret" >/dev/null
apply_socks_user inline-runtime-fallback runtime-socks-password
retry "$ALMAGEST_E2E_TIMEOUT" "mutable secret selector in the SOCKS menu" \
	menu_secret_is runtime-socks-password \
	|| fail "SOCKS menu did not retain the mutable secret selector"
assert_socks_success 11097 runtime-password-one mutable-secret-one \
	"request with the first mutable secret value"
assert_socks_failure 11097 inline-runtime-fallback mutable-inline-rejected \
	"request with the mutable secret inline fallback"

cli secrets set --key runtime-socks-password --value runtime-password-two >/dev/null
assert_socks_success 11097 runtime-password-two mutable-secret-two \
	"request after mutable secret rotation"
assert_socks_failure 11097 runtime-password-one mutable-secret-one-stale \
	"request with the stale mutable secret value"

# Renaming a selected secret must update both the live worker and the persisted
# selector. A later update to the new name proves the renamed selector is used.
cli secrets rename --key runtime-socks-password \
	--new-key runtime-socks-password-renamed >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "renamed secret selector in the SOCKS menu" \
	menu_secret_is runtime-socks-password-renamed \
	|| fail "SOCKS menu did not update its renamed secret selector"
assert_socks_success 11097 runtime-password-two renamed-secret \
	"request after selected secret rename"
cli secrets set --key runtime-socks-password-renamed \
	--value runtime-password-three >/dev/null
assert_socks_success 11097 runtime-password-three renamed-secret-rotation \
	"request after renamed secret rotation"

# A deleted selected secret falls back to the inline password. Recreating the
# same secret and allow list must replace the worker password again.
cli secrets delete --key runtime-socks-password-renamed >/dev/null
assert_socks_success 11097 inline-runtime-fallback deleted-secret-fallback \
	"request with inline fallback after secret deletion"
assert_socks_failure 11097 runtime-password-three deleted-secret-stale \
	"request with deleted secret value"
cli secrets set --key runtime-socks-password-renamed \
	--value runtime-password-four >/dev/null
cli secrets set-nodes --key runtime-socks-password-renamed \
	--node "SOCKS Server Secret" >/dev/null
assert_socks_success 11097 runtime-password-four recreated-secret \
	"request after selected secret recreation"

# Field validation and an invalid complete form must not replace the usable
# listener. The form error is observable while traffic still uses old state.
validation_message="$(jq -nc \
	--arg menu "NODE${server}MENU" \
	'{
		type:"field.validate",
		menuId:$menu,
		field:{blockId:"config",fieldId:"wsPort"},
		value:11097
	}')"
cli engine send-node-formular-msg --id "$server" \
	--message "$validation_message" >/dev/null
apply_socks_config \
	'{"config":{"wsHost":"127.0.0.1","wsPort":11097}}'
retry "$ALMAGEST_E2E_TIMEOUT" "SOCKS listener conflict form error" \
	menu_has_config_error \
	|| fail "SOCKS menu did not report its listener conflict"
assert_socks_success 11097 runtime-password-four invalid-form-preserves-runtime \
	"request after rejected listener configuration"

# A valid listener replacement must close the old address and authenticate on
# the new one. Disabling CONNECT must then reject traffic until it is enabled.
apply_socks_config '{"config":{"socksPort":11098,"wsPort":0}}'
wait_node_label "$server" "127.0.0.1:11098" \
	|| fail "SOCKS server did not accept its replacement listener address"
assert_socks_success 11098 runtime-password-four replacement-listener \
	"request through the replacement SOCKS listener"
assert_socks_failure 11097 runtime-password-four old-listener-closed \
	"request through the replaced SOCKS listener"

apply_socks_user inline-runtime-fallback runtime-socks-password-renamed false
assert_socks_failure 11098 runtime-password-four connect-disabled \
	"CONNECT request while the command is disabled"
apply_socks_user inline-runtime-fallback runtime-socks-password-renamed true
assert_socks_success 11098 runtime-password-four connect-restored \
	"CONNECT request after the command is enabled"

# The listener is independent from its optional proxy path. Removing proxy
# keeps accepts alive but rejects target access. Removing listen closes accepts.
proxy_link="$(link_id_by_port_names \
	"SOCKS Server Secret" proxy "OutNetwork SOCKS Proxy" Network)"
cli graph delete-link --id "$proxy_link" >/dev/null
assert_socks_failure 11098 runtime-password-four proxy-detached \
	"request while the proxy network is detached"
add_link_by_port_name "$server" right proxy "$proxy_network" left Network >/dev/null
assert_socks_success 11098 runtime-password-four proxy-restored \
	"first request after proxy network restoration"

listen_link="$(link_id_by_port_names \
	"SOCKS Server Secret" listen "OutNetwork SOCKS Listen" Network)"
cli graph delete-link --id "$listen_link" >/dev/null
assert_socks_failure 11098 runtime-password-four listen-detached \
	"request while the listen network is detached"
add_link_by_port_name "$server" right listen "$listen_network" left Network >/dev/null
assert_socks_success 11098 runtime-password-four listen-restored \
	"first request after listen network restoration"

grep -F "GET /listen-restored" "$target_log" >/dev/null \
	|| fail "external HTTP target did not receive the final restored request"

log "case passed"
