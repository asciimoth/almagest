#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

# common.sh owns this process ID. This assignment also makes the shared
# contract visible when shellcheck checks the case without following imports.
daemon_pid=""

export ALMAGEST_E2E_TIMEOUT=120
export CHUTNEY_DATA_DIR=/tmp/chutney
export CHUTNEY_DISABLE_IPV6=1
export CHUTNEY_DNS_CONF=/dev/null
export CHUTNEY_PATH=/opt/chutney
export CHUTNEY_START_TIME=300
export CHUTNEY_TOR=/usr/bin/tor
export CHUTNEY_TOR_GENCERT=/usr/bin/tor-gencert
export CHUTNEY_TOR_SANDBOX=0
export PATH="/opt/chutney-venv/bin:$PATH"

chutney=/opt/chutney/chutney
network=/e2e/cases/tor_driver_private/network.py
network_started=0
target_pid=""

run_chutney() {
	setpriv --reuid 1000 --regid 1000 --clear-groups -- \
		env \
		CHUTNEY_DATA_DIR="$CHUTNEY_DATA_DIR" \
		CHUTNEY_DISABLE_IPV6="$CHUTNEY_DISABLE_IPV6" \
		CHUTNEY_DNS_CONF="$CHUTNEY_DNS_CONF" \
		CHUTNEY_PATH="$CHUTNEY_PATH" \
		CHUTNEY_START_TIME="$CHUTNEY_START_TIME" \
		CHUTNEY_TOR="$CHUTNEY_TOR" \
		CHUTNEY_TOR_GENCERT="$CHUTNEY_TOR_GENCERT" \
		CHUTNEY_TOR_SANDBOX="$CHUTNEY_TOR_SANDBOX" \
		"$chutney" "$@"
}

show_chutney_logs() {
	find -L "$CHUTNEY_DATA_DIR/nodes" -name notice.log -type f \
		-print -exec tail -n 80 {} \; 2>/dev/null || true
}

cleanup() {
	local status="$?"
	trap - EXIT INT TERM
	stop_static_http_target "$target_pid"
	stop_daemon
	if ((network_started)); then
		run_chutney stop >/dev/null 2>&1 || true
	fi
	if ((status != 0)); then
		show_chutney_logs
	fi
	exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

rm -rf "$CHUTNEY_DATA_DIR"
install -d -o 1000 -g 1000 -m 700 "$CHUTNEY_DATA_DIR"
run_chutney init --net-from-script-path "$network"
run_chutney configure
run_chutney start --launch-phase 1
network_started=1
if ! run_chutney wait_for_bootstrap --launch-phase 1; then
	show_chutney_logs
	fail "private Tor relays did not bootstrap"
fi

client_torrc=""
for torrc in "$CHUTNEY_DATA_DIR"/nodes/*/torrc; do
	if grep -q '^SocksPort 127\.0\.0\.1:' "$torrc"; then
		client_torrc="$torrc"
		break
	fi
done
[[ -n "$client_torrc" ]] || fail "Chutney did not generate a client template"

export ALMAGEST_TOR_E2E_TORRC=/tmp/almagest-private-torrc
awk '
	$1 == "TestingTorNetwork" ||
	$1 == "AddressDisableIPv6" ||
	$1 == "UseMicrodescriptors" ||
	$1 == "DirAuthority" ||
	$1 == "AlternateDirAuthority" ||
	$1 == "AlternateBridgeAuthority"
' "$client_torrc" >"$ALMAGEST_TOR_E2E_TORRC"
chmod 0644 "$ALMAGEST_TOR_E2E_TORRC"

target_body="XTorDriver private Tor response"
target_log=/tmp/e2e-tor-private-target.log
target_pid="$(start_static_http_target 127.0.0.1 18092 "$target_body" "$target_log")"

start_daemon
[[ "$(id -u)" == 0 ]] || fail "case container is not running as root"
[[ "$(awk '/^Uid:/ { print $2 }' "/proc/$daemon_pid/status")" == 0 ]] \
	|| fail "almagestd is not running as root"

wait_node_class_available "almagest/XTorDriver" \
	|| fail "experimental Tor node class is unavailable"
tor_node="$(node_id_by_name "Experimental Tor")"
out_node="$(node_id_by_name "Tor outbound")"
wait_node_label "$tor_node" up || fail "XTorDriver did not become ready"
wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:1081" \
	|| fail "Tor SOCKS proxy did not start"

tor_pid=""
# shellcheck disable=SC2016 # $1 belongs to the inner sh command.
retry "$ALMAGEST_E2E_TIMEOUT" "Tor child process" sh -c \
	'test -n "$(pgrep -P "$1" -x tor | head -n 1)"' sh "$daemon_pid" \
	|| fail "almagestd did not spawn Tor"
tor_pid="$(pgrep -P "$daemon_pid" -x tor | head -n 1)"

read -r real_uid effective_uid saved_uid fs_uid < <(awk '/^Uid:/ { print $2, $3, $4, $5 }' "/proc/$tor_pid/status")
[[ "$real_uid" != 0 && "$real_uid" == "$effective_uid" \
	&& "$real_uid" == "$saved_uid" && "$real_uid" == "$fs_uid" ]] \
	|| fail "Tor child did not drop all root UIDs: $(grep '^Uid:' "/proc/$tor_pid/status")"
[[ "$(awk '/^CapEff:/ { print $2 }' "/proc/$tor_pid/status")" == 0000000000000000 ]] \
	|| fail "Tor child retained effective capabilities"
[[ "$(awk '/^CapAmb:/ { print $2 }' "/proc/$tor_pid/status")" == 0000000000000000 ]] \
	|| fail "Tor child retained ambient capabilities"
assert_daemon_log_contains "will drop Linux root privileges to automatically selected UID"

request_through_tor() {
	curl --fail --silent --show-error \
		--connect-timeout 3 --max-time 10 \
		--socks5-hostname 127.0.0.1:1081 \
		http://127.0.0.1:18092/private-tor \
		--output /tmp/e2e-tor-private-body.txt \
		&& [[ "$(cat /tmp/e2e-tor-private-body.txt)" == "$target_body" ]]
}

retry "$ALMAGEST_E2E_TIMEOUT" "HTTP request through private Tor" request_through_tor \
	|| fail "private Tor request did not succeed"

outbound_link="$(link_id_by_port_names "Experimental Tor" out "Tor outbound" Network)"
cli graph delete-link --id "$outbound_link" >/dev/null
wait_node_label "$tor_node" "waiting for Network" \
	|| fail "XTorDriver did not notice outbound removal"
if request_through_tor; then
	fail "Tor request succeeded without an outbound Network"
fi
kill -0 "$tor_pid" 2>/dev/null || fail "Tor process stopped after outbound removal"

add_link_by_port_name "$tor_node" right out "$out_node" left Network >/dev/null
wait_node_label "$tor_node" up || fail "XTorDriver did not recover its outbound Network"
retry "$ALMAGEST_E2E_TIMEOUT" "HTTP request after Tor outbound reattach" request_through_tor \
	|| fail "private Tor request did not recover"
[[ "$(pgrep -P "$daemon_pid" -x tor | head -n 1)" == "$tor_pid" ]] \
	|| fail "outbound reattach restarted Tor"

log "case passed"
