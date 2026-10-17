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

private_torrc=/tmp/almagest-private-torrc
awk '
	$1 == "TestingTorNetwork" ||
	$1 == "AddressDisableIPv6" ||
	$1 == "UseMicrodescriptors" ||
	$1 == "DirAuthority" ||
	$1 == "AlternateDirAuthority" ||
	$1 == "AlternateBridgeAuthority"
' "$client_torrc" >"$private_torrc"
chmod 0644 "$private_torrc"
python3 - "$private_torrc" protected_alm.hujson <<'PY'
import json
import os
import pathlib
import re
import subprocess
import sys

arguments = []
for raw in pathlib.Path(sys.argv[1]).read_text(encoding="utf-8").splitlines():
    key, value = raw.split(maxsplit=1)
    arguments.extend(({"Literal": "--" + key}, {"Literal": value}))
argument_replacement = ",\n".join("\t\t\t\t\t" + json.dumps(item) for item in arguments)

libraries = []
output = subprocess.run(["ldd", "/usr/bin/tor"], check=True, capture_output=True, text=True).stdout
for raw in output.splitlines():
    match = re.match(r"\s*\S+\s+=>\s+(/\S+)", raw)
    if match is None:
        match = re.match(r"\s*(/\S+)\s+\(", raw)
    if match is None:
        continue
    target = match.group(1)
    libraries.append({"Source": os.path.realpath(target), "Target": target, "Executable": True})
library_replacement = ",\n".join("\t\t\t\t\t" + json.dumps(item) for item in libraries)

path = pathlib.Path(sys.argv[2])
text = path.read_text(encoding="utf-8")
replacements = {
    "\t\t\t\t\t// %E2E_TOR_ARGUMENTS%": argument_replacement,
    "\t\t\t\t\t// %E2E_TOR_READ_ONLY%": library_replacement,
}
for marker, replacement in replacements.items():
    if text.count(marker) != 1:
        raise SystemExit(f"Tor policy marker is missing or duplicated: {marker.strip()}")
    text = text.replace(marker, replacement)
path.write_text(text, encoding="utf-8")
PY

target_body="TorDriver private Tor response"
target_log=/tmp/e2e-tor-private-target.log
target_pid="$(start_static_http_target 127.0.0.1 18092 "$target_body" "$target_log")"

find_sandbox_tor_pid() {
	local executable pid uid
	pid="$(ps -C tor -o pid=,uid= | awk '$2 != 1000 { print $1; exit }')"
	if [[ -n "$pid" ]]; then
		printf '%s\n' "$pid"
		return 0
	fi
	for executable in /proc/[0-9]*/exe; do
		[[ "$(readlink "$executable" 2>/dev/null || true)" == /usr/bin/tor ]] || continue
		pid="${executable#/proc/}"
		pid="${pid%/exe}"
		uid="$(awk '/^Uid:/ { print $2 }' "/proc/$pid/status" 2>/dev/null || true)"
		if [[ -n "$uid" && "$uid" != 1000 ]]; then
			printf '%s\n' "$pid"
			return 0
		fi
	done
	return 1
}

sandbox_tor_running() {
	find_sandbox_tor_pid >/dev/null
}

sandbox_tor_replaced() {
	local previous="$1"
	local current
	current="$(find_sandbox_tor_pid)" || return 1
	[[ "$current" != "$previous" ]] && ! kill -0 "$previous" 2>/dev/null
}

request_through_tor() {
	curl --fail --silent --show-error \
		--connect-timeout 3 --max-time 10 \
		--socks5-hostname 127.0.0.1:1081 \
		http://127.0.0.1:18092/private-tor \
		--output /tmp/e2e-tor-private-body.txt \
		&& [[ "$(cat /tmp/e2e-tor-private-body.txt)" == "$target_body" ]]
}

run_sandbox_mode() {
	local ALMAGEST_E2E_SANDBOX_MODE="$1"
	local tor_node out_node tor_pid real_uid effective_uid saved_uid fs_uid outbound_link
	python3 - "$ALMAGEST_E2E_SANDBOX_MODE" protected_alm.hujson <<'PY'
import pathlib
import re
import sys

path = pathlib.Path(sys.argv[2])
text = path.read_text(encoding="utf-8")
text, count = re.subn(r'("Mode"\s*:\s*")(?:off|auto|strict)(")', rf'\g<1>{sys.argv[1]}\2', text, count=1)
if count != 1:
    raise SystemExit("sandbox Mode field is missing or duplicated")
path.write_text(text, encoding="utf-8")
PY

	log "qualifying TorDriver with sandbox=$ALMAGEST_E2E_SANDBOX_MODE"
	start_daemon
	[[ "$(id -u)" == 0 ]] || fail "case container is not running as root"
	[[ "$(awk '/^Uid:/ { print $2 }' "/proc/$daemon_pid/status")" == 0 ]] \
		|| fail "almagestd is not running as root"
	wait_node_class_available "almagest/TorDriver" \
		|| fail "Tor node class is unavailable"
	tor_node="$(node_id_by_name "Tor")"
	out_node="$(node_id_by_name "Tor outbound")"
	wait_node_label "$tor_node" "waiting for Network" \
		|| fail "TorDriver did not wait for its initial outbound Network"
	wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:1081" \
		|| fail "Tor SOCKS proxy did not start"
	retry "$ALMAGEST_E2E_TIMEOUT" "sandboxed Tor process" sandbox_tor_running \
		|| fail "Tor sandbox did not start Tor"
	tor_pid="$(find_sandbox_tor_pid)"
	# Leave Tor without an outbound route long enough for real proxy failures.
	# A late input must replace that offline runtime and publish its Networks.
	sleep 3
	[[ "$(node_label "$tor_node")" == "waiting for Network" ]] \
		|| fail "TorDriver changed state while its initial outbound Network was absent"
	add_link_by_port_name "$tor_node" right out "$out_node" left Network >/dev/null
	wait_node_label "$tor_node" up \
		|| fail "TorDriver did not recover after its initial outbound Network was attached"
	retry "$ALMAGEST_E2E_TIMEOUT" "clean Tor restart after offline bootstrap" \
		sandbox_tor_replaced "$tor_pid" \
		|| fail "initial outbound attachment did not replace the offline Tor process"
	tor_pid="$(find_sandbox_tor_pid)"

	read -r real_uid effective_uid saved_uid fs_uid < <(awk '/^Uid:/ { print $2, $3, $4, $5 }' "/proc/$tor_pid/status")
	if [[ "$ALMAGEST_E2E_SANDBOX_MODE" != off ]]; then
		[[ "$real_uid" != 0 && "$real_uid" == "$effective_uid" \
			&& "$real_uid" == "$saved_uid" && "$real_uid" == "$fs_uid" ]] \
			|| fail "Tor child did not drop all root UIDs: $(grep '^Uid:' "/proc/$tor_pid/status")"
		[[ "$(awk '/^CapEff:/ { print $2 }' "/proc/$tor_pid/status")" == 0000000000000000 ]] \
			|| fail "Tor child retained effective capabilities"
		[[ "$(awk '/^CapAmb:/ { print $2 }' "/proc/$tor_pid/status")" == 0000000000000000 ]] \
			|| fail "Tor child retained ambient capabilities"
	fi
	cli admin hello --client-name tor-driver-private >/tmp/tor-driver-private-hello.json
	if [[ "$ALMAGEST_E2E_SANDBOX_MODE" == off ]]; then
		jq -e '
			.execution_mode == "isolation"
			and .subprogram_sandbox.mode == "off"
			and .subprogram_sandbox.templates.tor.profile == "direct-v1"
			and (.subprogram_sandbox.templates.tor.strict_satisfied | not)
			and (.subprogram_sandbox.templates.tor.full_boundary | not)
		' /tmp/tor-driver-private-hello.json >/dev/null
	else
		jq -e --arg mode "$ALMAGEST_E2E_SANDBOX_MODE" '
			.execution_mode == "isolation"
			and .subprogram_sandbox.mode == $mode
			and .subprogram_sandbox.templates.tor.strict_satisfied
			and .subprogram_sandbox.templates.tor.full_boundary
			and .subprogram_sandbox.templates.tor.effective_identity == "65534:65534"
		' /tmp/tor-driver-private-hello.json >/dev/null
	fi

	retry "$ALMAGEST_E2E_TIMEOUT" "HTTP request through private Tor" request_through_tor \
		|| fail "private Tor request did not succeed"
	outbound_link="$(link_id_by_port_names "Tor" out "Tor outbound" Network)"
	cli graph delete-link --id "$outbound_link" >/dev/null
	wait_node_label "$tor_node" "waiting for Network" \
		|| fail "TorDriver did not notice outbound removal"
	if request_through_tor; then
		fail "Tor request succeeded without an outbound Network"
	fi
	kill -0 "$tor_pid" 2>/dev/null || fail "Tor process stopped after outbound removal"

	add_link_by_port_name "$tor_node" right out "$out_node" left Network >/dev/null
	wait_node_label "$tor_node" up || fail "TorDriver did not recover its outbound Network"
	retry "$ALMAGEST_E2E_TIMEOUT" "HTTP request after Tor outbound reattach" request_through_tor \
		|| fail "private Tor request did not recover"
	[[ "$(find_sandbox_tor_pid)" == "$tor_pid" ]] \
		|| fail "outbound reattach restarted Tor"
	# Keep the saved graph disconnected so the next sandbox-mode daemon also
	# exercises an initially missing outbound Network.
	outbound_link="$(link_id_by_port_names "Tor" out "Tor outbound" Network)"
	cli graph delete-link --id "$outbound_link" >/dev/null
	wait_node_label "$tor_node" "waiting for Network" \
		|| fail "TorDriver did not withdraw before the next sandbox-mode run"
	stop_daemon
	log "sandbox=$ALMAGEST_E2E_SANDBOX_MODE passed"
}

for sandbox_mode in off auto strict; do
	run_sandbox_mode "$sandbox_mode"
done
log "case passed"
