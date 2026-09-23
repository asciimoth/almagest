#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

curl_err="/tmp/e2e-socks-chain-nested-curl.err"
curl_out="/tmp/e2e-socks-chain-nested-body.txt"

nested_socks_request() {
	curl -g --fail --silent --show-error \
		--connect-timeout 1 --max-time 3 \
		--socks5-hostname 127.0.0.1:1081 \
		http://127.0.0.1:8080 \
		--output "$curl_out" \
		2>"$curl_err"
}

nested_socks_request_fails() {
	! nested_socks_request
}

assert_nested_request() {
	local description="$1"
	if ! retry "$ALMAGEST_E2E_TIMEOUT" "$description" nested_socks_request; then
		if [[ -s "$curl_err" ]]; then
			sed 's/^/[curl] /' "$curl_err" >&2
		fi
		fail "$description did not succeed"
	fi
	[[ "$(<"$curl_out")" == "Debug http server response" ]] \
		|| fail "$description returned an unexpected response"
}

assert_nested_failure() {
	local description="$1"
	retry "$ALMAGEST_E2E_TIMEOUT" "$description" nested_socks_request_fails \
		|| fail "$description stayed usable"
}

set_socks_client_url() {
	local node="$1"
	local value="$2"
	local menu_id
	local message
	menu_id="$(cli graph menu --id "$node" | jq -er '.menuId')"
	message="$(jq -cn \
		--arg menu "$menu_id" \
		--arg value "$value" \
		'{
			type: "field.update",
			menuId: $menu,
			field: {blockId: "config", fieldId: "proxyURL"},
			value: $value
		}')"
	cli engine send-node-formular-msg --id "$node" --message "$message" >/dev/null
}

start_daemon

wait_graph_node "LocalProxy 2013" || fail "public LocalProxy node missing"
wait_graph_node "SocksClient 2096" || fail "nested SocksClient node missing"
wait_graph_node "SocksClient 2015" || fail "chain SocksClient node missing"
wait_graph_node "LocalProxy 2020" || fail "chain LocalProxy node missing"
wait_graph_node "SocksClient 2022" || fail "second chain SocksClient node missing"
wait_graph_node "LocalProxy 2027" || fail "second chain LocalProxy node missing"
wait_graph_node "LocalProxy 2094" || fail "mock LocalProxy node missing"
wait_graph_node "MockNetwork 2035" || fail "MockNetwork node missing"
wait_graph_node "HttpServer 2043" || fail "HttpServer node missing"

wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:1081" \
	|| fail "public local proxy did not start listening"
wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:1082" \
	|| fail "chain local proxy did not start listening"
wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:1083" \
	|| fail "second chain local proxy did not start listening"
wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:1084" \
	|| fail "mock local proxy did not start listening"
wait_log_contains "almagest/HttpServer" "listening on 127.0.0.1:8080" \
	|| fail "mock HTTP server did not start listening"

assert_nested_request "initial nested SOCKS request to mock HTTP server"

wait_log_contains "almagest/LocalProxy" "connect 127.0.0.1:8080" \
	|| fail "local proxy did not log the final HTTP connect request"
wait_log_contains "almagest/LocalProxy" "connect 127.0.0.1:1084" \
	|| fail "local proxy did not log the nested SOCKS connect request"
wait_log_contains "almagest/HttpServer" "GET /" \
	|| fail "HTTP server did not log the proxied request"

nested_client="$(node_id_by_name "SocksClient 2096")"
chain_client="$(node_id_by_name "SocksClient 2015")"
chain_out="$(node_id_by_name "OutNetwork 2088")"

# A malformed URL must close the old output wrapper and report an error. A
# valid replacement must rebuild the full nested path for its first request.
set_socks_client_url "$nested_client" '://invalid-proxy-url'
wait_node_label "$nested_client" "invalid proxy URL" \
	|| fail "nested SocksClient did not reject the malformed URL"
assert_nested_failure "request with malformed nested proxy URL"
set_socks_client_url "$nested_client" 'socks5://127.0.0.1:1084'
wait_node_label "$nested_client" 'socks5://127.0.0.1:1084' \
	|| fail "nested SocksClient did not accept the restored URL"
assert_nested_request "first request after nested proxy URL recovery"

# Rapid URL replacement must converge on the last value. Old wrappers must
# not close or overwrite the current output after it becomes usable.
for value in \
	'socks5://127.0.0.1:1098' \
	'socks5://127.0.0.1:1084' \
	'socks5://127.0.0.1:1099' \
	'socks5://127.0.0.1:1084'; do
	set_socks_client_url "$nested_client" "$value"
done
wait_node_label "$nested_client" 'socks5://127.0.0.1:1084' \
	|| fail "nested SocksClient did not converge on the latest URL"
assert_nested_request "request after rapid nested proxy URL changes"

# Removing the immediate out network must invalidate all dependent wrappers.
# Apply the desired URL while detached, then restore the edge and require the
# first complete-chain request to use the current client.
nested_out_link="$(link_id_by_port_names \
	"SocksClient 2096" out "SocksClient 2015" Network)"
cli graph delete-link --id "$nested_out_link" >/dev/null
wait_node_label "$nested_client" "waiting for out" \
	|| fail "nested SocksClient did not wait after out-network removal"
assert_nested_failure "request after nested out-network removal"
set_socks_client_url "$nested_client" 'socks5://127.0.0.1:1099'
set_socks_client_url "$nested_client" 'socks5://127.0.0.1:1084'
add_link_by_port_name "$nested_client" right out "$chain_client" left Network >/dev/null
wait_node_label "$nested_client" 'socks5://127.0.0.1:1084' \
	|| fail "nested SocksClient did not attach its replacement out network"
assert_nested_request "first request after nested out-network replacement"

# Replace the next upstream edge to verify that a down event propagates
# through both SocksClient layers and that recovery also propagates back.
chain_out_link="$(link_id_by_port_names \
	"SocksClient 2015" out "OutNetwork 2088" Network)"
cli graph delete-link --id "$chain_out_link" >/dev/null
wait_node_label "$chain_client" "waiting for out" \
	|| fail "chain SocksClient did not wait after upstream removal"
assert_nested_failure "request after chained upstream removal"
add_link_by_port_name "$chain_client" right out "$chain_out" left Network >/dev/null
wait_node_label "$chain_client" 'socks5://127.0.0.1:1082' \
	|| fail "chain SocksClient did not attach its replacement upstream"
assert_nested_request "first request after chained upstream replacement"

# Saved URL values and restored graph edges must reconstruct the chain.
stop_daemon
start_daemon
nested_client="$(node_id_by_name "SocksClient 2096")"
wait_node_label "$nested_client" 'socks5://127.0.0.1:1084' \
	|| fail "nested SocksClient did not restore its URL"
assert_nested_request "first nested SOCKS request after daemon restart"

log "case passed"
