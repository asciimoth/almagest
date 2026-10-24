#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

target_body="curl through LocalProxy secret"
target_log="$E2E_RUNTIME/local-proxy-secret-target.log"
target_pid=""
curl_error="$E2E_RUNTIME/local-proxy-secret-curl.err"

cleanup() {
	stop_static_http_target "$target_pid"
	stop_daemon
}

proxy_request() {
	local password="$1"
	local path="$2"
	local output="$E2E_RUNTIME/local-proxy-${path}.body"

	curl --fail --silent --show-error \
		--connect-timeout 1 --max-time 3 \
		--socks5-hostname 127.0.0.1:11099 \
		--proxy-user "alice:$password" \
		"http://127.0.0.1:18099/$path" \
		--output "$output" \
		2>"$curl_error" \
		&& [[ "$(<"$output")" == "$target_body" ]]
}

proxy_request_fails() {
	! proxy_request "$@"
}

assert_proxy_success() {
	local password="$1"
	local path="$2"
	local description="$3"

	retry "$ALMAGEST_E2E_TIMEOUT" "$description" \
		proxy_request "$password" "$path" \
		|| fail "$description did not succeed"
}

assert_proxy_failure() {
	local password="$1"
	local path="$2"
	local description="$3"

	retry "$ALMAGEST_E2E_TIMEOUT" "$description" \
		proxy_request_fails "$password" "$path" \
		|| fail "$description unexpectedly succeeded"
}

apply_proxy_user() {
	local inline_password="$1"
	local secret_name="$2"
	local values

	values="$(jq -nc \
		--arg inline "$inline_password" \
		--arg secret "$secret_name" \
		'{config:{users:[{
			id:"alice",
			template:"user",
			values:{
				username:"alice",
				password:$inline,
				passwordSecret:$secret
			}
		}]}}')"
	cli graph apply-menu --name "Local Proxy Secret" --json "$values" >/dev/null
}

menu_secret_is() {
	local expected="$1"
	cli graph menu --name "Local Proxy Secret" | jq -e --arg expected "$expected" '
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

target_pid="$(start_static_http_target 127.0.0.1 18099 "$target_body" "$target_log")"
start_daemon
trap cleanup EXIT

wait_graph_node "Local Proxy Secret" || fail "LocalProxy node missing"
wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:11099" \
	|| fail "LocalProxy did not start listening"

assert_proxy_success protected-password protected-secret \
	"request with the protected secret password"
assert_proxy_failure wrong-inline-password protected-inline-rejected \
	"request with the overridden inline password"

cli secrets set --key runtime-local-password --value runtime-one >/dev/null
cli secrets set-nodes --key runtime-local-password \
	--node "Local Proxy Secret" >/dev/null
apply_proxy_user inline-fallback runtime-local-password
assert_proxy_success runtime-one mutable-secret-one \
	"request with the first mutable secret value"
assert_proxy_failure inline-fallback mutable-inline-rejected \
	"request with the overridden mutable fallback"

cli secrets set --key runtime-local-password --value runtime-two >/dev/null
assert_proxy_success runtime-two mutable-secret-two \
	"request after mutable secret rotation"
assert_proxy_failure runtime-one stale-secret-rejected \
	"request with the stale secret value"

cli secrets rename --key runtime-local-password \
	--new-key runtime-local-password-renamed >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "renamed LocalProxy secret selector" \
	menu_secret_is runtime-local-password-renamed \
	|| fail "LocalProxy menu did not update its renamed secret selector"
assert_proxy_success runtime-two renamed-secret \
	"request after selected secret rename"

cli secrets delete --key runtime-local-password-renamed >/dev/null
assert_proxy_success inline-fallback deleted-secret-fallback \
	"request with inline fallback after secret deletion"
assert_proxy_failure runtime-two deleted-secret-rejected \
	"request with the deleted secret value"

cli secrets set --key runtime-local-password-renamed --value runtime-three >/dev/null
cli secrets set-nodes --key runtime-local-password-renamed \
	--node "Local Proxy Secret" >/dev/null
assert_proxy_success runtime-three recreated-secret \
	"request after selected secret recreation"

grep -F "GET /recreated-secret" "$target_log" >/dev/null \
	|| fail "external HTTP target did not receive the final request"

log "case passed"
