#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

control_file="$E2E_RUNTIME/fetch-control"
access_log="$E2E_RUNTIME/fetch-access.log"
server_ready="$E2E_RUNTIME/fetch-server.ready"
server_error="$E2E_RUNTIME/fetch-server.stderr"
server_pid=""

set_mode() {
	printf '%s\n' "$1" >"$control_file"
}

start_fetch_server() {
	set_mode valid
	rm -f "$access_log" "$server_ready" "$server_error"
	python3 -u - \
		"$control_file" \
		"$access_log" \
		"$server_ready" \
		"$E2E_CONFIG_DIR/fetch-server.pem" \
		"$E2E_CONFIG_DIR/fetch-server.key" \
		<<'PY' >/dev/null 2>"$server_error" &
import http.server
import pathlib
import ssl
import sys
import time

control = pathlib.Path(sys.argv[1])
access_log = pathlib.Path(sys.argv[2])
ready = pathlib.Path(sys.argv[3])
cert = sys.argv[4]
key = sys.argv[5]


def record(text):
    with access_log.open("a", encoding="utf-8") as out:
        out.write(text + "\n")


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_GET(self):
        mode = control.read_text(encoding="utf-8").strip()
        record(f"start mode={mode} host={self.headers.get('Host', '')} path={self.path}")

        if mode.startswith("slow_"):
            time.sleep(1.25)

        if mode == "status_503":
            body = b"provider unavailable"
            status = 503
        elif mode == "invalid_ip":
            body = b'{"ip":"not-an-address","country_iso":"zz","city":"bad"}'
            status = 200
        elif mode == "oversized":
            body = b"x" * ((1 << 20) + 1)
            status = 200
        elif mode.startswith("slow_"):
            body = b'{"ip":"192.0.2.199","country_iso":"ZZ","city":"stale"}'
            status = 200
        elif self.path.startswith("/json"):
            body = b'{"ip":" 2001:0db8:0:0::42 ","country_iso":" ge ","city":" Tbilisi ","latitude":41.7151,"longitude":44.8271}'
            status = 200
        else:
            body = b'{"ip":"2001:0db8:0:0::55"}'
            status = 200

        try:
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.send_header("Connection", "close")
            self.end_headers()
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError, ssl.SSLError):
            pass
        record(f"finish mode={mode} path={self.path}")

    def log_message(self, fmt, *args):
        return


server = http.server.ThreadingHTTPServer(("127.0.0.1", 443), Handler)
context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
context.load_cert_chain(cert, key)
server.socket = context.wrap_socket(server.socket, server_side=True)
ready.touch(exist_ok=False)
server.serve_forever()
PY
	server_pid="$!"
	wait_static_http_target_ready "$server_pid" "$server_ready" "$server_error" \
		|| fail "local FetchAddr HTTPS service did not become ready"
}

cleanup() {
	trap - EXIT
	stop_daemon
	stop_static_http_target "$server_pid"
}

menu_value() {
	local block="$1"
	local field="$2"
	cli graph menu --name "Fetch Address" | jq -er \
		--arg block "$block" \
		--arg field "$field" \
		'
		.blocks[]
		| select(.id == $block)
		| .items[]
		| select(.id == $field)
		| (.value // .text // "")
		'
}

fetch_success_is() {
	local ip="$1"
	local country="$2"
	local city="$3"
	local latitude="$4"
	local longitude="$5"
	[[ "$(menu_value result status)" == "Status: success" \
		&& -z "$(menu_value result error)" \
		&& "$(menu_value result publicIP)" == "$ip" \
		&& "$(menu_value result countryCode)" == "$country" \
		&& "$(menu_value result city)" == "$city" \
		&& "$(menu_value result latitude)" == "$latitude" \
		&& "$(menu_value result longitude)" == "$longitude" ]]
}

fetch_failure_contains() {
	local text="$1"
	[[ "$(menu_value result status)" == "Status: fail" \
		&& "$(menu_value result error)" == *"$text"* \
		&& -z "$(menu_value result publicIP)" \
		&& -z "$(menu_value result countryCode)" \
		&& -z "$(menu_value result city)" \
		&& "$(menu_value result latitude)" == "0" \
		&& "$(menu_value result longitude)" == "0" ]]
}

fetch_ready_and_empty() {
	[[ "$(menu_value result status)" == "Status: ready" \
		&& -z "$(menu_value result error)" \
		&& -z "$(menu_value result publicIP)" ]]
}

missing_tls_policy_warns() {
	cli graph menu --name "Fetch Address" | jq -e '
		.blocks[]
		| select(.id == "check")
		| .items[]
		| select(.id == "tlsConfig")
		| .value == "default"
			and .status == "warn"
			and (.statusText | contains("missing-fetch-policy"))
	' >/dev/null
}

click_check() {
	cli graph click-menu-button \
		--name "Fetch Address" \
		--block check \
		--button check \
		>/dev/null
}

set_fetch_field() {
	local field="$1"
	local value="$2"
	local menu_id
	local message
	menu_id="$(cli graph menu --name "Fetch Address" | jq -er '.menuId')"
	message="$(jq -cn \
		--arg menu "$menu_id" \
		--arg field "$field" \
		--arg value "$value" \
		'{
			type: "field.update",
			menuId: $menu,
			field: {blockId: "check", fieldId: $field},
			value: $value
		}')"
	cli engine send-node-formular-msg \
		--name "Fetch Address" \
		--message "$message" \
		>/dev/null
}

assert_fetch_success() {
	local ip="$1"
	local country="$2"
	local city="$3"
	local latitude="$4"
	local longitude="$5"
	retry "$ALMAGEST_E2E_TIMEOUT" "FetchAddr success for $ip" \
		fetch_success_is "$ip" "$country" "$city" "$latitude" "$longitude" \
		|| fail "FetchAddr did not publish the expected result for $ip"
}

assert_fetch_failure() {
	local text="$1"
	retry "$ALMAGEST_E2E_TIMEOUT" "FetchAddr failure containing $text" \
		fetch_failure_contains "$text" \
		|| fail "FetchAddr did not fail with an error containing: $text"
}

wait_server_record() {
	local text="$1"
	retry "$ALMAGEST_E2E_TIMEOUT" "FetchAddr service log containing $text" \
		grep -F -- "$text" "$access_log" >/dev/null
}

start_fetch_server
trap cleanup EXIT
start_daemon
trap cleanup EXIT

fetch_id="$(node_id_by_name "Fetch Address")"
out_id="$(node_id_by_name "Public Network")"
wait_node_label "$fetch_id" ready || fail "FetchAddr did not become ready"

# Send one request after the advertised startup state. A request retry would
# hide a first-request readiness problem.
click_check
assert_fetch_success "2001:db8::42" GE Tbilisi 41.7151 44.8271

# A provider that only returns an address must clear all optional values from
# the previous result.
set_fetch_field provider ipify
[[ "$(menu_value check provider)" == ipify ]] || fail "FetchAddr did not select ipify"
set_mode valid
click_check
assert_fetch_success "2001:db8::55" "" "" 0 0

# Invalid provider names normalize to the default provider.
set_fetch_field provider not-a-provider
[[ "$(menu_value check provider)" == "ifconfig.co" ]] \
	|| fail "FetchAddr did not normalize an invalid provider"

set_mode status_503
click_check
assert_fetch_failure "provider returned 503"

set_mode invalid_ip
click_check
assert_fetch_failure "valid public IP address"

set_mode oversized
click_check
assert_fetch_failure "provider response is too large"

# A missing selected TLS policy uses the default policy and warns in the menu.
# The default policy does not trust this test CA, so TLS must fail before the
# provider receives an HTTP request. Restore the valid policy afterwards.
requests_before="$(wc -l <"$access_log")"
set_fetch_field tlsConfig missing-fetch-policy
retry "$ALMAGEST_E2E_TIMEOUT" "missing TLS policy warning" missing_tls_policy_warns \
	|| fail "FetchAddr did not report its default-policy fallback"
click_check
assert_fetch_failure "certificate signed by unknown authority"
requests_after="$(wc -l <"$access_log")"
[[ "$requests_after" == "$requests_before" ]] \
	|| fail "FetchAddr sent HTTP data before default-policy TLS verification failed"
set_fetch_field tlsConfig fetch-address-test

# A second check while the first is pending must not enqueue or run another
# provider request.
set_mode slow_coalesce
click_check
wait_server_record "start mode=slow_coalesce"
click_check
wait_server_record "finish mode=slow_coalesce"
assert_fetch_success "192.0.2.199" ZZ stale 0 0
[[ "$(grep -cF 'start mode=slow_coalesce' "$access_log")" == 1 ]] \
	|| fail "FetchAddr started more than one request while pending"

# Changing provider cancels the old generation. Its delayed completion must
# not replace the newer ipify result.
set_mode slow_provider_change
click_check
wait_server_record "start mode=slow_provider_change"
set_fetch_field provider ipify
set_mode valid
click_check
assert_fetch_success "2001:db8::55" "" "" 0 0
wait_server_record "finish mode=slow_provider_change"
assert_fetch_success "2001:db8::55" "" "" 0 0

# Detaching the network also invalidates a pending generation. Reattaching the
# same provider must permit a new request, and the stale result stays ignored.
set_fetch_field provider ifconfig.co
set_mode slow_network_detach
click_check
wait_server_record "start mode=slow_network_detach"
network_link="$(link_id_by_port_names "Fetch Address" Network "Public Network" Network)"
cli graph delete-link --id "$network_link" >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "FetchAddr reset after network detach" \
	fetch_ready_and_empty \
	|| fail "FetchAddr did not reset after its network was detached"

add_link_by_port_name "$fetch_id" right Network "$out_id" left Network >/dev/null
set_mode valid
click_check
assert_fetch_success "2001:db8::42" GE Tbilisi 41.7151 44.8271
wait_server_record "finish mode=slow_network_detach"
assert_fetch_success "2001:db8::42" GE Tbilisi 41.7151 44.8271

log "case passed"
