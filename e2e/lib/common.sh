#!/usr/bin/env bash
set -euo pipefail

: "${E2E_CASE:?}"
: "${E2E_RUNTIME:?}"
: "${E2E_CONFIG_DIR:?}"
: "${E2E_ADMIN_SOCK:?}"
: "${E2E_DAEMON_LOG:?}"

daemon_pid=""
ALMAGEST_E2E_TIMEOUT="${ALMAGEST_E2E_TIMEOUT:-20}"
# Bound shared CLI calls so admin/socket stalls fail the case with logs instead
# of hanging the whole e2e run.
ALMAGEST_E2E_CLI_TIMEOUT="${ALMAGEST_E2E_CLI_TIMEOUT:-5}"
ALMAGEST_E2E_LOG_CACHE_DEBUG="${ALMAGEST_E2E_LOG_CACHE_DEBUG:-16384}"
ALMAGEST_E2E_LOG_CACHE_INFO="${ALMAGEST_E2E_LOG_CACHE_INFO:-4096}"
ALMAGEST_E2E_LOG_CACHE_WARN="${ALMAGEST_E2E_LOG_CACHE_WARN:-4096}"
ALMAGEST_E2E_LOG_CACHE_ERROR="${ALMAGEST_E2E_LOG_CACHE_ERROR:-2048}"
ALMAGEST_E2E_LOG_CACHE_FATAL="${ALMAGEST_E2E_LOG_CACHE_FATAL:-512}"

log() {
	printf '[%s] %s\n' "$E2E_CASE" "$*" >&2
}

fail() {
	log "FAILED: $*"
	if [[ -f "$E2E_DAEMON_LOG" ]]; then
		log "daemon log follows"
		tail -200 "$E2E_DAEMON_LOG" >&2 || true
	fi
	exit 1
}

retry() {
	local timeout="$1"
	local description="$2"
	shift 2
	local deadline=$((SECONDS + timeout))
	local status=0
	while ((SECONDS <= deadline)); do
		"$@" && return 0
		status=$?
		sleep 0.25
	done
	log "timed out waiting for $description"
	return "$status"
}

cli() {
	timeout --kill-after=2 "$ALMAGEST_E2E_CLI_TIMEOUT" \
		almagest-cli --url "unix://$E2E_ADMIN_SOCK" "$@"
}

start_daemon() {
	if [[ -n "$daemon_pid" ]] && kill -0 "$daemon_pid" 2>/dev/null; then
		fail "daemon is already running"
	fi
	mkdir -p "$E2E_RUNTIME"
	: >"$E2E_DAEMON_LOG"
	log "starting daemon"
	XDG_RUNTIME_DIR="$E2E_RUNTIME" almagestd \
		-c "$E2E_CONFIG_DIR" \
		-l debug \
		--debug-nodes \
		--unsafe-config \
		--tmp-file-storage \
		--log-cache-debug "$ALMAGEST_E2E_LOG_CACHE_DEBUG" \
		--log-cache-info "$ALMAGEST_E2E_LOG_CACHE_INFO" \
		--log-cache-warn "$ALMAGEST_E2E_LOG_CACHE_WARN" \
		--log-cache-error "$ALMAGEST_E2E_LOG_CACHE_ERROR" \
		--log-cache-fatal "$ALMAGEST_E2E_LOG_CACHE_FATAL" \
		--unix "$E2E_ADMIN_SOCK" \
		--web 0 \
		>"$E2E_DAEMON_LOG" 2>&1 &
	daemon_pid="$!"
	trap stop_daemon EXIT
	retry "$ALMAGEST_E2E_TIMEOUT" "admin API socket" test -S "$E2E_ADMIN_SOCK" \
		|| fail "admin API socket was not created"
	retry "$ALMAGEST_E2E_TIMEOUT" "admin API ping" \
		cli admin ping --nonce "$E2E_CASE" >/tmp/e2e-ping.json \
		|| fail "admin API did not answer ping"
	jq -e --arg nonce "$E2E_CASE" '.nonce == $nonce' /tmp/e2e-ping.json >/dev/null \
		|| fail "admin API ping returned unexpected payload"
}

stop_daemon() {
	trap - EXIT
	if [[ -z "${daemon_pid:-}" ]]; then
		return 0
	fi
	if kill -0 "$daemon_pid" 2>/dev/null; then
		log "stopping daemon"
		kill "$daemon_pid" 2>/dev/null || true
		local deadline=$((SECONDS + 10))
		while kill -0 "$daemon_pid" 2>/dev/null && ((SECONDS <= deadline)); do
			sleep 0.25
		done
		if kill -0 "$daemon_pid" 2>/dev/null; then
			kill -KILL "$daemon_pid" 2>/dev/null || true
		fi
	fi
	wait "$daemon_pid" 2>/dev/null || true
	daemon_pid=""
}

node_id_by_name() {
	local name="$1"
	cli graph map | jq -er --arg name "$name" '.nodes[$name].ID'
}

wait_graph_node() {
	local name="$1"
	retry "$ALMAGEST_E2E_TIMEOUT" "graph node $name" \
		node_id_by_name "$name" >/dev/null
}

node_class_available() {
	local class="$1"
	cli graph classes | jq -e --arg class "$class" '.classes[] | select(.class == $class)' >/dev/null
}

wait_node_class_available() {
	local class="$1"
	retry "$ALMAGEST_E2E_TIMEOUT" "node class $class" \
		node_class_available "$class"
}

log_contains() {
	local class="$1"
	local text="$2"
	cli log subscribe | jq -e --arg class "$class" --arg text "$text" \
		'.entries[] | select(.node_class == $class and (.message | contains($text)))' >/dev/null
}

wait_log_contains() {
	local class="$1"
	local text="$2"
	retry "$ALMAGEST_E2E_TIMEOUT" "log entry from $class containing $text" \
		log_contains "$class" "$text"
}

assert_log_not_contains() {
	local class="$1"
	local text="$2"
	if cli log subscribe | jq -e --arg class "$class" --arg text "$text" \
		'.entries[] | select(.node_class == $class and (.message | contains($text)))' >/dev/null; then
		fail "unexpected log entry from $class containing $text"
	fi
}

assert_daemon_log_contains() {
	local text="$1"
	grep -F -- "$text" "$E2E_DAEMON_LOG" >/dev/null || fail "daemon log does not contain: $text"
}

node_label() {
	local id="$1"
	cli graph node --id "$id" | jq -er '.snapshot.label'
}

node_label_is() {
	local id="$1"
	local label="$2"
	[[ "$(node_label "$id")" == "$label" ]]
}

wait_node_label() {
	local id="$1"
	local label="$2"
	retry "$ALMAGEST_E2E_TIMEOUT" "node $id label $label" \
		node_label_is "$id" "$label"
}

add_node() {
	local class="$1"
	local name="$2"
	cli engine add-node --class "$class" --name "$name" | jq -er '.id'
}

node_port_id() {
	local node="$1"
	local direction="$2"
	local name="$3"

	cli engine workspace-snapshot | jq -er \
		--arg node "$node" \
		--arg direction "$direction" \
		--arg name "$name" \
		'
		.snapshot as $workspace
		| $workspace.nodes[$node] as $nodeSnapshot
		| ($nodeSnapshot[$direction + "_ports"] // [])[]
		| . as $portID
		| $workspace.ports[$portID | tostring]
		| select(.name == $name)
		| $portID
		'
}

link_id_by_port_names() {
	local right_node="$1"
	local right_port_name="$2"
	local left_node="$3"
	local left_port_name="$4"

	cli engine workspace-snapshot | jq -er \
		--arg right_node "$right_node" \
		--arg right_port_name "$right_port_name" \
		--arg left_node "$left_node" \
		--arg left_port_name "$left_port_name" \
		'
		.snapshot as $workspace
		| ($workspace.nodes | to_entries[] | select(.value.name == $right_node) | .key) as $rightNodeID
		| ($workspace.nodes | to_entries[] | select(.value.name == $left_node) | .key) as $leftNodeID
		| $workspace.links
		| to_entries[]
		| select(
			(.value.right_port_node | tostring) == $rightNodeID
			and (.value.left_port_node | tostring) == $leftNodeID
			and $workspace.ports[(.value.right_port | tostring)].name == $right_port_name
			and $workspace.ports[(.value.left_port | tostring)].name == $left_port_name
		)
		| .key
		'
}

add_link_by_port_name() {
	local node_a="$1"
	local direction_a="$2"
	local port_a_name="$3"
	local node_b="$4"
	local direction_b="$5"
	local port_b_name="$6"

	local port_a
	local port_b
	port_a="$(node_port_id "$node_a" "$direction_a" "$port_a_name")"
	port_b="$(node_port_id "$node_b" "$direction_b" "$port_b_name")"
	cli graph add-link --port-a "$port_a" --port-b "$port_b" | jq -er '.id'
}

start_static_http_target() {
	local host="$1"
	local port="$2"
	local body="$3"
	local access_log="$4"

	rm -f "$access_log"
	python3 -u - "$host" "$port" "$body" "$access_log" <<'PY' >/dev/null 2>"$access_log.stderr" &
import http.server
import sys

host = sys.argv[1]
port = int(sys.argv[2])
body = sys.argv[3].encode()
access_log = sys.argv[4]


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        with open(access_log, "a", encoding="utf-8") as out:
            out.write(f"{self.command} {self.path} {self.request_version}\n")
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        return


server = http.server.ThreadingHTTPServer((host, port), Handler)
server.serve_forever()
PY
	echo "$!"
}

start_static_http_target_in_netns() {
	local netns="$1"
	local host="$2"
	local port="$3"
	local body="$4"
	local access_log="$5"

	rm -f "$access_log"
	ip netns exec "$netns" python3 -u -c '
import http.server
import sys

host = sys.argv[1]
port = int(sys.argv[2])
body = sys.argv[3].encode()
access_log = sys.argv[4]


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        with open(access_log, "a", encoding="utf-8") as out:
            out.write(f"{self.command} {self.path} {self.request_version}\n")
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        return


server = http.server.ThreadingHTTPServer((host, port), Handler)
server.serve_forever()
' "$host" "$port" "$body" "$access_log" >/dev/null 2>"$access_log.stderr" &
	echo "$!"
}

stop_static_http_target() {
	local pid="$1"
	if [[ -n "$pid" ]]; then
		kill "$pid" 2>/dev/null || true
		wait "$pid" 2>/dev/null || true
	fi
}

ensure_e2e_user() {
	local name="$1"
	local uid="$2"
	local gid="${3:-$uid}"

	if ! awk -F: -v name="$name" '$1 == name { found=1 } END { exit !found }' /etc/group; then
		printf '%s:x:%s:\n' "$name" "$gid" >>/etc/group
	fi
	if ! awk -F: -v name="$name" '$1 == name { found=1 } END { exit !found }' /etc/passwd; then
		printf '%s:x:%s:%s::/nonexistent:/usr/sbin/nologin\n' "$name" "$uid" "$gid" >>/etc/passwd
	fi
}

run_as_uid() {
	local uid="$1"
	local gid="${2:-$uid}"
	shift 2
	setpriv --reuid "$uid" --regid "$gid" --clear-groups -- "$@"
}
