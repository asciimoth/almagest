#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

# common.sh owns this process ID. This assignment makes the shared contract
# visible when shellcheck checks this case without following imports.
daemon_pid=""

export ALMAGEST_E2E_TIMEOUT=180
export ALMAGEST_E2E_CLI_TIMEOUT=10
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
network=/e2e/cases/tor_driver_onion_http/network.py
network_started=0
client_menu=/tmp/e2e-tor-onion-http-client-menu.json

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

start_daemon

wait_node_class_available "almagest/TorDriver" \
	|| fail "Tor node class is unavailable"
cli admin hello --client-name tor-driver-onion >/tmp/tor-driver-onion-hello.json
jq -e '
	.execution_mode == "isolation"
	and .subprogram_sandbox.templates.tor.strict_satisfied
	and .subprogram_sandbox.templates.tor.full_boundary
' /tmp/tor-driver-onion-hello.json >/dev/null
for name in \
	"Tor service" \
	"Tor client" \
	"Onion HTTP server" \
	"Onion HTTP client" \
	"Onion service address" \
	"Onion service URL" \
	"Tor outbound"; do
	wait_graph_node "$name" || fail "$name node is missing"
done

service_tor="$(node_id_by_name "Tor service")"
client_tor="$(node_id_by_name "Tor client")"
http_client="$(node_id_by_name "Onion HTTP client")"

# The two graph nodes must own two different sandboxed Tor processes. Chutney
# uses UID 1000. A private user namespace can map the configured sandbox UID
# to a different UID in this parent namespace.
# shellcheck disable=SC2016 # The command substitution and awk fields belong to the inner shell.
retry "$ALMAGEST_E2E_TIMEOUT" "two Tor child processes" sh -c \
	'test "$(ps -C tor -o uid= | awk '\''$1 != 1000 { count++ } END { print count + 0 }'\'')" -eq 2' \
	|| fail "almagestd did not start two Tor child processes"
mapfile -t tor_pids < <(ps -C tor -o pid=,uid= | awk '$2 != 1000 { print $1 }' | sort -n)
[[ "${#tor_pids[@]}" == 2 && "${tor_pids[0]}" != "${tor_pids[1]}" ]] \
	|| fail "Tor nodes do not have distinct child processes"

wait_node_label "$client_tor" up || fail "client Tor node did not become ready"
wait_node_label "$service_tor" up || fail "onion service was not published"

onion_address="$(cli graph menu --node "$service_tor" | jq -er '
	.blocks[]
	| select(.id == "status")
	| .items[]
	| select(.id == "services")
	| .value
	| split("\n")[]
	| select(startswith("Test service | ") and endswith(" | published"))
	| split(" | ")[1]
	| select(test("^[a-z2-7]{56}\\.onion$"))
')" || fail "service status does not contain a published v3 onion address"

# The empty service selector must have been replaced with a stored secret.
service_key_secret="$(cli graph menu --node "$service_tor" | jq -er '
	.blocks[]
	| select(.id == "Onion services")
	| .items[]
	| select(.id == "services")
	| .elements[]
	| select(any(.items[]; .id == "name" and .value == "Test service"))
	| .items[]
	| select(.id == "keySecret")
	| .value
	| select(length > 0)
')" || fail "onion service key selector was not saved"
cli secrets list | jq -e --arg key "$service_key_secret" \
	'.secrets[] | select(.key == $key and (.value | length > 0))' >/dev/null \
	|| fail "generated onion service key is missing from secrets storage"

# A service Network only accepts its own onion hostname. The constant nodes
# update the linked, read-only server Host and client URL fields.
jq -n --arg value "$onion_address" '{state:{value:$value}}' \
	| cli graph apply-menu --name "Onion service address" >/dev/null
jq -n --arg value "http://$onion_address/" '{state:{value:$value}}' \
	| cli graph apply-menu --name "Onion service URL" >/dev/null

wait_log_contains "almagest/HttpServer" "listening on $onion_address:80" \
	|| fail "HTTP server did not listen on the onion service Network"

client_result_snapshot() {
	cli graph menu --node "$http_client" >"$client_menu"
}

client_result_field() {
	local field="$1"
	jq -er --arg field "$field" '
		.blocks[]
		| select(.id == "result")
		| .items[]
		| select(.id == $field)
		| (.value // .text // "")
	' "$client_menu"
}

onion_request_succeeded() {
	local status
	local error
	local response
	client_result_snapshot || return 1
	status="$(client_result_field status)"
	if [[ "$status" != "Status: running..." ]]; then
		cli graph click-menu-button --node "$http_client" --block request --button send \
			>/tmp/e2e-tor-onion-http-click.json || return 1
	fi
	local deadline=$((SECONDS + 45))
	while ((SECONDS <= deadline)); do
		client_result_snapshot || return 1
		status="$(client_result_field status)"
		error="$(client_result_field error)"
		response="$(client_result_field response)"
		if [[ "$status" == "Status: 200 OK" && -z "$error" \
			&& "$response" == "Response through two Tor nodes" ]]; then
			return 0
		fi
		if [[ "$status" != "Status: running..." ]]; then
			return 1
		fi
		sleep 0.25
	done
	return 1
}

retry "$ALMAGEST_E2E_TIMEOUT" "HTTP request through the onion service" \
	onion_request_succeeded \
	|| fail "HTTP client did not receive the onion service response"
wait_node_label "$http_client" "200 OK" \
	|| fail "HTTP client node did not report 200 OK"
wait_log_contains "almagest/HttpServer" "GET /" \
	|| fail "HTTP server did not log the onion request"

# A request made by this client must use the client Tor process. The service
# Tor process remains alive and owns the published service throughout it.
for pid in "${tor_pids[@]}"; do
	kill -0 "$pid" 2>/dev/null || fail "Tor child $pid stopped during the request"
done

log "case passed"
