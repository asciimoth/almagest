#!/usr/bin/env bash
set -euo pipefail

format="${1:?package format is required}"
arch="${2:?package architecture is required}"

export PATH="/usr/libexec/almagest:$PATH"
export E2E_CASE="package-$format"
export E2E_RUNTIME="/run/almagest-package-test"
export E2E_CONFIG_DIR="/etc/almagest"
export E2E_ADMIN_SOCK="/run/almagest/admin.sock"
export E2E_DAEMON_LOG="$E2E_RUNTIME/daemon.log"
export E2E_ISOLATION_ROOT="$E2E_RUNTIME/isolation"
export ALMAGEST_E2E_DAEMON_MODE=direct
export ALMAGEST_E2E_TMP_FILE_STORAGE=0
app_log="$E2E_RUNTIME/app-state/almagest/logs/almagest-app.log"

mkdir -p "$E2E_RUNTIME" /run/almagest /var/lib/almagest

# Reuse the normal E2E daemon, retry, graph, and local-target helpers.
# shellcheck disable=SC1091
source /e2e/lib/common.sh

proxy_target_pid=""
local_target_pid=""
public_target_pid=""
app_pid=""
xvfb_pid=""

cleanup() {
	local status="$?"
	trap - EXIT
	stop_static_http_target "$proxy_target_pid"
	stop_static_http_target "$local_target_pid"
	stop_static_http_target "$public_target_pid"
	if [[ -n "$app_pid" ]]; then
		pkill --parent "$app_pid" 2>/dev/null || true
		kill "$app_pid" 2>/dev/null || true
	fi
	if [[ -n "$xvfb_pid" ]]; then kill "$xvfb_pid" 2>/dev/null || true; fi
	stop_daemon
	if [[ -x "$E2E_RUNTIME/cleanup-netns.sh" ]]; then
		"$E2E_RUNTIME/cleanup-netns.sh"
	fi
	exit "$status"
}
trap cleanup EXIT

json_object() {
	jq -e 'type == "object"' >/dev/null
}

proxy_body_is() {
	local output="$E2E_RUNTIME/proxy-body.txt"
	curl --fail --silent --show-error --max-time 3 \
		--socks5-hostname 127.0.0.1:1081 \
		http://127.0.0.1:18091/package-proxy \
		--output "$output" \
		&& [[ "$(<"$output")" == "packaged OutNetwork response" ]]
}

curl_target() {
	local target="$1"
	local output="$2"
	curl --fail --silent --show-error --connect-timeout 1 --max-time 3 \
		"http://$target/package-default-tun" --output "$output"
}

curl_target_fails() {
	local target="$1"
	! curl --fail --silent --show-error --connect-timeout 1 --max-time 1 \
		"http://$target/package-default-tun" --output /dev/null \
		2>"$E2E_RUNTIME/default-tun-expected-failure.log"
}

desktop_app_is_connected() {
	kill -0 "$app_pid" 2>/dev/null \
		&& [[ -f "$app_log" ]] \
		&& grep -F 'admin API connected transport_id=' \
			"$app_log" >/dev/null
}

start_daemon

if [[ "$arch" == amd64 ]]; then
	install -d -m 0700 "$E2E_RUNTIME/app-runtime"
	Xvfb :99 -screen 0 1024x768x24 >"$E2E_RUNTIME/xvfb.log" 2>&1 &
	xvfb_pid="$!"
	retry "$ALMAGEST_E2E_TIMEOUT" "virtual app display" \
		test -S /tmp/.X11-unix/X99 || fail "virtual app display did not start"
	DISPLAY=:99 XDG_RUNTIME_DIR="$E2E_RUNTIME/app-runtime" \
		XDG_STATE_HOME="$E2E_RUNTIME/app-state" \
		WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS=1 \
		dbus-run-session -- /usr/bin/almagest-app \
		>"$E2E_RUNTIME/app.log" 2>&1 &
	app_pid="$!"
	if ! retry "$ALMAGEST_E2E_TIMEOUT" "desktop app admin connection" \
		desktop_app_is_connected; then
		log "desktop app log follows"
		tail -200 "$app_log" >&2 || true
		log "desktop app process output follows"
		tail -200 "$E2E_RUNTIME/app.log" >&2 || true
		fail "desktop app did not connect to the packaged daemon"
	fi
fi

# The packaged systemd unit sends both process streams to the journal. Verify
# that the daemon writes its normal startup messages to the captured stream.
# The unit-file checks in smoke.sh verify the journal side of this contract.
retry "$ALMAGEST_E2E_TIMEOUT" "packaged daemon startup log" \
	grep -F -- 'Execution mode: direct' "$E2E_DAEMON_LOG" \
	|| fail "packaged daemon did not write its startup log"
grep -F -- 'Starting unix admin listener on /run/almagest/admin.sock' \
	"$E2E_DAEMON_LOG" >/dev/null \
	|| fail "packaged daemon did not log the admin listener"
[[ "$(stat --format '%G:%a' "$E2E_ADMIN_SOCK")" == "almagest:660" ]] \
	|| fail "packaged admin socket does not grant access to the almagest group"

# Probe every stable daemon surface with a small read operation. Mutations for
# config, KV, secrets, examples, and the graph are tested below.
cli admin colormap | json_object
cli config snapshot | json_object
cli dns caches | json_object
cli docs tree --locale en >"$E2E_RUNTIME/docs.json"
jq -e '.locale == "en" and ([.. | objects | .pages[]?] | length > 0)' \
	"$E2E_RUNTIME/docs.json" >/dev/null \
	|| fail "packaged localized documentation is unavailable"
cli engine workspace-snapshot | json_object
cli graph classes | jq -e \
	'.classes | length > 0 and all(.[]; (.class | type == "string" and length > 0))' \
	>/dev/null
cli kv stores | json_object
cli log subscribe | json_object
cli secrets list | json_object
cli snapshots list >"$E2E_RUNTIME/examples.json"
jq -e 'any(.snapshots[]; (.example | type == "string" and length > 0))' \
	"$E2E_RUNTIME/examples.json" >/dev/null \
	|| fail "packaged examples are unavailable"
cli tls list | json_object
cli workers snapshot | json_object

# Adapt the local_proxy_out_http E2E fixture to verify config replacement,
# LocalProxy, and OutNetwork with the installed daemon and CLI.
proxy_target_pid="$(start_static_http_target \
	127.0.0.1 18091 "packaged OutNetwork response" \
	"$E2E_RUNTIME/proxy-target.log")"
cli config snapshot >"$E2E_RUNTIME/config-before.json"
revision="$(jq -er '.revision' "$E2E_RUNTIME/config-before.json")"
config_before="$(sha256sum /etc/almagest/alm.hujson | cut -d ' ' -f 1)"
cli config apply --revision "$revision" \
	/e2e/cases/local_proxy_out_http/alm.hujson \
	>"$E2E_RUNTIME/config-applied.json"
[[ "$(sha256sum /etc/almagest/alm.hujson | cut -d ' ' -f 1)" != "$config_before" ]] \
	|| fail "config API did not replace the config file"
grep -F -- 'LocalProxy 1101' /etc/almagest/alm.hujson >/dev/null \
	|| fail "config API did not persist the applied graph"
wait_graph_node "LocalProxy 1101" || fail "LocalProxy from applied config is missing"
wait_graph_node "OutNetwork 1103" || fail "OutNetwork from applied config is missing"
retry "$ALMAGEST_E2E_TIMEOUT" "packaged LocalProxy and OutNetwork request" \
	proxy_body_is || fail "packaged LocalProxy could not use OutNetwork"

# Exercise mutable storage systems before a restart.
cli kv set --key package-retained --data package-kv-value >/dev/null
cli kv get --key package-retained | \
	jq -e '.found and .value.data == "package-kv-value"' >/dev/null \
	|| fail "packaged KV value did not round-trip"
cli secrets set --key package-retained --value package-secret-value >/dev/null
cli secrets list | jq -e \
	'.secrets[] | select(.key == "package-retained" and .value == "package-secret-value")' \
	>/dev/null || fail "packaged secret did not round-trip"

stop_daemon
start_daemon

wait_graph_node "LocalProxy 1101" || fail "API-applied graph was not retained"
retry "$ALMAGEST_E2E_TIMEOUT" "LocalProxy request after restart" \
	proxy_body_is || fail "LocalProxy graph did not recover after restart"
cli kv get --key package-retained | \
	jq -e '.found and .value.data == "package-kv-value"' >/dev/null \
	|| fail "KV value was not retained after restart"
cli secrets list | jq -e \
	'.secrets[] | select(.key == "package-retained" and .value == "package-secret-value")' \
	>/dev/null || fail "secret was not retained after restart"

# Load one packaged example and verify that its graph returns after a daemon
# restart. The example catalog is content, so do not bind this package test to
# a specific example ID or graph shape.
example_id="$(jq -er 'first(.snapshots[] | select(.example | type == "string" and length > 0)).example' \
	"$E2E_RUNTIME/examples.json")"
cli snapshots load "$example_id" >"$E2E_RUNTIME/example-loaded.json"
jq -e --arg example "$example_id" '.snapshot.example == $example' \
	"$E2E_RUNTIME/example-loaded.json" >/dev/null \
	|| fail "packaged example load returned unexpected metadata"
example_node="$(cli graph map | jq -er '.nodes | keys | first')"
stop_daemon
start_daemon
wait_graph_node "$example_node" || fail "loaded example was not retained after restart"

# QEMU user-mode emulation supports route netlink but can reject the XFRM and
# netfilter families that netlink.NewHandle probes. Native hosts normally
# support them. Keep the portable package and daemon checks above, but do not
# treat this emulator limitation as a DefaultTun failure.
if ! python3 - 2>/dev/null <<'PY'
import socket

for protocol in (socket.NETLINK_ROUTE, 6, 12):
    handle = socket.socket(socket.AF_NETLINK, socket.SOCK_RAW, protocol)
    handle.close()
PY
then
	printf 'skipping DefaultTun package test on %s: required netlink families are unavailable\n' "$arch"
	stop_daemon
	exit 0
fi

# Adapt the short path through default_tun_routing. A public-prefix target is
# blocked by an unattached DefaultTun, then becomes reachable through
# DefaultTun -> Spoofer -> OutNetwork.
/e2e/cases/default_tun_routing/setup.sh
local_target_pid="$(start_static_http_target_in_netns \
	alm-e2e-local 10.213.1.2 18093 "packaged local route" \
	"$E2E_RUNTIME/default-tun-local.log")"
public_target_pid="$(start_static_http_target_in_netns \
	alm-e2e-public 203.0.113.2 18094 "packaged public route" \
	"$E2E_RUNTIME/default-tun-public.log")"
retry "$ALMAGEST_E2E_TIMEOUT" "DefaultTun baseline local target" \
	curl_target 10.213.1.2:18093 "$E2E_RUNTIME/default-tun-local-before.txt" \
	|| fail "DefaultTun local target is unavailable before config apply"
retry "$ALMAGEST_E2E_TIMEOUT" "DefaultTun baseline public target" \
	curl_target 203.0.113.2:18094 "$E2E_RUNTIME/default-tun-public-before.txt" \
	|| fail "DefaultTun public target is unavailable before config apply"

revision="$(cli config snapshot | jq -er '.revision')"
cli config apply --revision "$revision" \
	/e2e/cases/default_tun_routing/alm.hujson \
	>"$E2E_RUNTIME/default-tun-config.json"
wait_graph_node "DefaultTun 1301" || fail "DefaultTun from applied config is missing"
default_tun="$(node_id_by_name "DefaultTun 1301")"
wait_node_label "$default_tun" ready || fail "DefaultTun did not become ready"
retry "$ALMAGEST_E2E_TIMEOUT" "public target blocked by unattached DefaultTun" \
	curl_target_fails 203.0.113.2:18094 \
	|| fail "public target bypassed unattached DefaultTun"

spoofer="$(add_node almagest/Spoofer "Package Spoofer")"
out_network="$(add_node almagest/OutNetwork "Package OutNetwork")"
add_link_by_port_name "$default_tun" right Tun "$spoofer" left Tun >/dev/null
add_link_by_port_name "$spoofer" right Network "$out_network" left Network >/dev/null
wait_node_label "$spoofer" forwarding || fail "packaged Spoofer did not start"
retry "$ALMAGEST_E2E_TIMEOUT" "public target through packaged DefaultTun" \
	curl_target 203.0.113.2:18094 "$E2E_RUNTIME/default-tun-public-after.txt" \
	|| fail "packaged DefaultTun could not reach OutNetwork"
grep -F -- 'packaged public route' "$E2E_RUNTIME/default-tun-public-after.txt" >/dev/null \
	|| fail "packaged DefaultTun returned an unexpected response"

stop_daemon
"$E2E_RUNTIME/cleanup-netns.sh"
rm -f "$E2E_RUNTIME/cleanup-netns.sh"

printf 'package integration test passed: %s\n' "$format"
