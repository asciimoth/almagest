#!/usr/bin/env bash
set -euo pipefail

: "${E2E_CASE:?}"
: "${E2E_RUNTIME:?}"
: "${E2E_CONFIG_DIR:?}"
: "${E2E_ADMIN_SOCK:?}"
: "${E2E_DAEMON_LOG:?}"
: "${E2E_ISOLATION_ROOT:?}"

daemon_pid=""
daemon_cgroup=""
daemon_log_reported=0
daemon_start_count=0
# Parallel coverage runs can need more than 30 seconds to start an isolated
# daemon when several graph-heavy cases run at the same time. This limit also
# covers secondary daemons started by multi-daemon cases.
ALMAGEST_E2E_TIMEOUT="${ALMAGEST_E2E_TIMEOUT:-60}"
# Bound shared CLI calls so admin/socket stalls fail the case instead of
# hanging the whole e2e run. Coverage builds can need more than five seconds
# for valid config and storage operations when cases run in parallel.
ALMAGEST_E2E_CLI_TIMEOUT="${ALMAGEST_E2E_CLI_TIMEOUT:-20}"
ALMAGEST_E2E_LOG_CACHE_DEBUG="${ALMAGEST_E2E_LOG_CACHE_DEBUG:-16384}"
ALMAGEST_E2E_LOG_CACHE_INFO="${ALMAGEST_E2E_LOG_CACHE_INFO:-4096}"
ALMAGEST_E2E_LOG_CACHE_WARN="${ALMAGEST_E2E_LOG_CACHE_WARN:-4096}"
ALMAGEST_E2E_LOG_CACHE_ERROR="${ALMAGEST_E2E_LOG_CACHE_ERROR:-2048}"
ALMAGEST_E2E_LOG_CACHE_FATAL="${ALMAGEST_E2E_LOG_CACHE_FATAL:-512}"
ALMAGEST_E2E_TMP_FILE_STORAGE="${ALMAGEST_E2E_TMP_FILE_STORAGE:-1}"
ALMAGEST_E2E_FILE_STORAGE="${ALMAGEST_E2E_FILE_STORAGE:-}"
ALMAGEST_E2E_WEB_ADMIN="${ALMAGEST_E2E_WEB_ADMIN:-0}"
ALMAGEST_E2E_WEB_PPROF="${ALMAGEST_E2E_WEB_PPROF:-0}"
ALMAGEST_E2E_STARTUP_SNAPSHOT="${ALMAGEST_E2E_STARTUP_SNAPSHOT:-}"
ALMAGEST_E2E_DAEMON_MODE="${ALMAGEST_E2E_DAEMON_MODE:-isolation}"
ALMAGEST_E2E_ISOLATION_MODE="${ALMAGEST_E2E_ISOLATION_MODE:-auto}"
ALMAGEST_E2E_ISOLATION_UID="${ALMAGEST_E2E_ISOLATION_UID:-10001}"
ALMAGEST_E2E_ISOLATION_GID="${ALMAGEST_E2E_ISOLATION_GID:-10001}"
ALMAGEST_E2E_WRITER_UID="${ALMAGEST_E2E_WRITER_UID:-10002}"
ALMAGEST_E2E_WRITER_GID="${ALMAGEST_E2E_WRITER_GID:-10002}"
ALMAGEST_E2E_COMMAND_UID="${ALMAGEST_E2E_COMMAND_UID:-10003}"
ALMAGEST_E2E_COMMAND_GID="${ALMAGEST_E2E_COMMAND_GID:-10003}"

log() {
	printf '[%s] %s\n' "$E2E_CASE" "$*" >&2
}

fail() {
	log "FAILED: $*"
	if [[ -f "$E2E_DAEMON_LOG" ]]; then
		log "daemon log follows"
		tail -200 "$E2E_DAEMON_LOG" >&2 || true
		daemon_log_reported=1
	fi
	for previous_log in "$E2E_DAEMON_LOG".*; do
		[[ -f "$previous_log" ]] || continue
		log "previous daemon log $previous_log follows"
		tail -200 "$previous_log" >&2 || true
	done
	if [[ -S "$E2E_ADMIN_SOCK" ]]; then
		log "cached node logs follow"
		cli log subscribe >&2 || true
		log "graph map follows"
		cli graph map >&2 || true
		log "node menus follow"
		while IFS= read -r node_id; do
			cli graph menu --id "$node_id" >&2 || true
		done < <(cli graph map | jq -r '.nodes[].ID' 2>/dev/null || true)
	fi
	exit 1
}

cleanup_daemon() {
	local status="$?"

	trap - EXIT
	if ((status != 0 && daemon_log_reported == 0)) && [[ -f "$E2E_DAEMON_LOG" ]]; then
		log "daemon log follows after unexpected case exit"
		tail -200 "$E2E_DAEMON_LOG" >&2 || true
	fi
	stop_daemon
	exit "$status"
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
	local status

	if timeout --kill-after=2 "$ALMAGEST_E2E_CLI_TIMEOUT" \
		almagest-cli --url "unix://$E2E_ADMIN_SOCK" "$@"; then
		return 0
	else
		status=$?
	fi
	if ((status == 124)); then
		log "CLI timed out after ${ALMAGEST_E2E_CLI_TIMEOUT}s: $*"
	fi
	return "$status"
}

start_daemon() {
	if [[ -n "$daemon_pid" ]] && kill -0 "$daemon_pid" 2>/dev/null; then
		fail "daemon is already running"
	fi
	mkdir -p "$E2E_RUNTIME"
	if [[ -s "$E2E_DAEMON_LOG" ]]; then
		cp "$E2E_DAEMON_LOG" "$E2E_DAEMON_LOG.$daemon_start_count"
	fi
	((daemon_start_count += 1)) || true
	: >"$E2E_DAEMON_LOG"
	log "starting daemon"
	local storage_args=()
	local config_args=(-c "$E2E_CONFIG_DIR")
	local web_args=(--web "$ALMAGEST_E2E_WEB_ADMIN")
	local snapshot_args=()
	local mode_args=()
	local isolation_root="$E2E_ISOLATION_ROOT"
	local daemon_environment=()
	local daemon_command=()
	if [[ "$ALMAGEST_E2E_DAEMON_MODE" == "isolation" ]]; then
		mode_args=(--isolation="$ALMAGEST_E2E_ISOLATION_MODE")
		if [[ "$ALMAGEST_E2E_TMP_FILE_STORAGE" == "1" ]]; then
			ALMAGEST_E2E_TMP_FILE_STORAGE=0
		fi
		mkdir -p "$isolation_root" "$isolation_root/config" "$isolation_root/protected" \
			"$isolation_root/runtime" "$isolation_root/helper" "$isolation_root/broker"
		if [[ -n "$ALMAGEST_E2E_FILE_STORAGE" ]]; then
			mkdir -p "$ALMAGEST_E2E_FILE_STORAGE"
			if ! mountpoint -q "$isolation_root/runtime"; then
				mount --bind "$ALMAGEST_E2E_FILE_STORAGE" "$isolation_root/runtime"
			fi
		fi
		install -o 0 -g 0 -m 0600 "$E2E_CONFIG_DIR/protected_alm.hujson" \
			"$isolation_root/protected/protected_alm.hujson"
		chown "$ALMAGEST_E2E_WRITER_UID:$ALMAGEST_E2E_WRITER_GID" "$E2E_CONFIG_DIR/alm.hujson"
		chmod 0600 "$E2E_CONFIG_DIR/alm.hujson"
		chown "$ALMAGEST_E2E_WRITER_UID:$ALMAGEST_E2E_WRITER_GID" "$isolation_root/config"
		chown "$ALMAGEST_E2E_ISOLATION_UID:$ALMAGEST_E2E_ISOLATION_GID" "$isolation_root/runtime"
		chown "$ALMAGEST_E2E_COMMAND_UID:$ALMAGEST_E2E_COMMAND_GID" "$isolation_root/helper"
		chown 0:0 "$isolation_root" "$isolation_root/protected" "$isolation_root/broker"
		chmod 0755 "$isolation_root"
		chmod 0700 "$isolation_root/config" "$isolation_root/protected" \
			"$isolation_root/runtime" "$isolation_root/helper" "$isolation_root/broker"
		daemon_environment=(
			"ALMAGEST_ISOLATION_DEVELOPMENT_ROOT=$isolation_root"
			"ALMAGEST_ISOLATION_DEVELOPMENT_IDENTITIES=$ALMAGEST_E2E_ISOLATION_UID:$ALMAGEST_E2E_ISOLATION_GID,$ALMAGEST_E2E_WRITER_UID:$ALMAGEST_E2E_WRITER_GID,$ALMAGEST_E2E_COMMAND_UID:$ALMAGEST_E2E_COMMAND_GID"
		)
		config_args=()
	else
		mode_args=(--isolation=off --experimental)
	fi
	if [[ "$ALMAGEST_E2E_TMP_FILE_STORAGE" == "1" ]]; then
		storage_args=(--tmp-file-storage)
	elif [[ -n "$ALMAGEST_E2E_FILE_STORAGE" && "$ALMAGEST_E2E_DAEMON_MODE" == "direct" ]]; then
		mkdir -p "$ALMAGEST_E2E_FILE_STORAGE"
		chmod 700 "$ALMAGEST_E2E_FILE_STORAGE"
		storage_args=(--file-storage "$ALMAGEST_E2E_FILE_STORAGE")
	fi
	if [[ "$ALMAGEST_E2E_WEB_PPROF" == "1" ]]; then
		if [[ "$ALMAGEST_E2E_WEB_ADMIN" == "0" ]]; then
			fail "ALMAGEST_E2E_WEB_PPROF requires a nonzero web admin port"
		fi
		web_args+=(--web-pprof)
	fi
	if [[ -n "$ALMAGEST_E2E_STARTUP_SNAPSHOT" ]]; then
		snapshot_args=(--snapshot "$ALMAGEST_E2E_STARTUP_SNAPSHOT")
	fi
	daemon_command=(env "${daemon_environment[@]}" XDG_RUNTIME_DIR="$E2E_RUNTIME" almagestd \
		"${mode_args[@]}" \
		"${config_args[@]}" \
		-l debug \
		--debug-nodes \
		--unsafe-config \
		"${storage_args[@]}" \
		--log-cache-debug "$ALMAGEST_E2E_LOG_CACHE_DEBUG" \
		--log-cache-info "$ALMAGEST_E2E_LOG_CACHE_INFO" \
		--log-cache-warn "$ALMAGEST_E2E_LOG_CACHE_WARN" \
		--log-cache-error "$ALMAGEST_E2E_LOG_CACHE_ERROR" \
		--log-cache-fatal "$ALMAGEST_E2E_LOG_CACHE_FATAL" \
		--unix "$E2E_ADMIN_SOCK" \
		"${snapshot_args[@]}" \
		"${web_args[@]}")
	if [[ "$ALMAGEST_E2E_DAEMON_MODE" == "isolation" ]]; then
		[[ -n "${E2E_CGROUP_PARENT:-}" ]] || fail "delegated E2E cgroup parent is unavailable"
		local cgroup_suffix="${ALMAGEST_E2E_ISOLATION_MODE}-${ALMAGEST_E2E_SANDBOX_MODE:-auto}"
		daemon_cgroup="$E2E_CGROUP_PARENT/almagest-e2e-daemon-$cgroup_suffix"
		mkdir "$daemon_cgroup" || fail "could not create daemon cgroup"
		(
			printf '%s\n' "$BASHPID" >"$daemon_cgroup/cgroup.procs"
			exec "${daemon_command[@]}"
		) >"$E2E_DAEMON_LOG" 2>&1 &
	else
		"${daemon_command[@]}" >"$E2E_DAEMON_LOG" 2>&1 &
	fi
	daemon_pid="$!"
	trap cleanup_daemon EXIT
	retry "$ALMAGEST_E2E_TIMEOUT" "admin API socket" test -S "$E2E_ADMIN_SOCK" \
		|| fail "admin API socket was not created"
	retry "$ALMAGEST_E2E_TIMEOUT" "admin API ping" \
		cli admin ping --nonce "$E2E_CASE" >/tmp/e2e-ping.json \
		|| fail "admin API did not answer ping"
	jq -e --arg nonce "$E2E_CASE" '.nonce == $nonce' /tmp/e2e-ping.json >/dev/null \
		|| fail "admin API ping returned unexpected payload"
	cli admin hello --client-name "e2e-$E2E_CASE" >/tmp/e2e-hello.json \
		|| fail "admin API did not answer hello"
	if [[ "$ALMAGEST_E2E_DAEMON_MODE" == "isolation" ]]; then
		jq -e '.execution_mode == "isolation" and .isolation.protocol_version > 0' \
			/tmp/e2e-hello.json >/dev/null \
			|| fail "daemon did not report an active isolation profile"
	else
		jq -e '.execution_mode == "direct" and .isolation == null' \
			/tmp/e2e-hello.json >/dev/null \
			|| fail "daemon did not report direct mode"
	fi
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
	if [[ -n "$daemon_cgroup" ]]; then
		rmdir "$daemon_cgroup" 2>/dev/null || true
		daemon_cgroup=""
	fi
}

# Start one additional isolated daemon for cases that exercise communication
# between independent daemon instances. The caller owns process cleanup.
start_isolated_daemon_instance() {
	local suffix="${1:?instance suffix is required}"
	local source_config="${2:?source config directory is required}"
	local admin_socket="${3:?admin socket is required}"
	local daemon_log="${4:?daemon log is required}"
	local pid_var="${5:?PID variable name is required}"
	local cgroup_var="${6:?cgroup variable name is required}"
	local isolation_root="$E2E_RUNTIME/isolation-$suffix"
	local instance_cgroup="$E2E_CGROUP_PARENT/almagest-e2e-daemon-$suffix"

	[[ "$ALMAGEST_E2E_DAEMON_MODE" == "isolation" ]] \
		|| fail "multi-daemon isolation helper requires isolation mode"
	[[ -n "${E2E_CGROUP_PARENT:-}" ]] \
		|| fail "delegated E2E cgroup parent is unavailable"
	mkdir -p "$isolation_root/config" "$isolation_root/protected" \
		"$isolation_root/runtime" "$isolation_root/helper" "$isolation_root/broker"
	install -o "$ALMAGEST_E2E_WRITER_UID" -g "$ALMAGEST_E2E_WRITER_GID" -m 0600 \
		"$source_config/alm.hujson" "$isolation_root/config/alm.hujson"
	install -o 0 -g 0 -m 0600 "$source_config/protected_alm.hujson" \
		"$isolation_root/protected/protected_alm.hujson"
	chown "$ALMAGEST_E2E_WRITER_UID:$ALMAGEST_E2E_WRITER_GID" "$isolation_root/config"
	chown "$ALMAGEST_E2E_ISOLATION_UID:$ALMAGEST_E2E_ISOLATION_GID" "$isolation_root/runtime"
	chown "$ALMAGEST_E2E_COMMAND_UID:$ALMAGEST_E2E_COMMAND_GID" "$isolation_root/helper"
	chown 0:0 "$isolation_root" "$isolation_root/protected" "$isolation_root/broker"
	chmod 0755 "$isolation_root"
	chmod 0700 "$isolation_root/config" "$isolation_root/protected" \
		"$isolation_root/runtime" "$isolation_root/helper" "$isolation_root/broker"
	mkdir "$instance_cgroup" || fail "could not create daemon $suffix cgroup"
	: >"$daemon_log"
	(
		printf '%s\n' "$BASHPID" >"$instance_cgroup/cgroup.procs"
		exec env \
			"ALMAGEST_ISOLATION_DEVELOPMENT_ROOT=$isolation_root" \
			"ALMAGEST_ISOLATION_DEVELOPMENT_IDENTITIES=$ALMAGEST_E2E_ISOLATION_UID:$ALMAGEST_E2E_ISOLATION_GID,$ALMAGEST_E2E_WRITER_UID:$ALMAGEST_E2E_WRITER_GID,$ALMAGEST_E2E_COMMAND_UID:$ALMAGEST_E2E_COMMAND_GID" \
			XDG_RUNTIME_DIR="$E2E_RUNTIME" almagestd \
			-l debug --debug-nodes --unsafe-config \
			--log-cache-debug "$ALMAGEST_E2E_LOG_CACHE_DEBUG" \
			--log-cache-info "$ALMAGEST_E2E_LOG_CACHE_INFO" \
			--log-cache-warn "$ALMAGEST_E2E_LOG_CACHE_WARN" \
			--log-cache-error "$ALMAGEST_E2E_LOG_CACHE_ERROR" \
			--log-cache-fatal "$ALMAGEST_E2E_LOG_CACHE_FATAL" \
			--unix "$admin_socket" --web 0
	) >"$daemon_log" 2>&1 &
	printf -v "$pid_var" '%s' "$!"
	printf -v "$cgroup_var" '%s' "$instance_cgroup"
	retry "$ALMAGEST_E2E_TIMEOUT" "daemon $suffix admin socket" test -S "$admin_socket" \
		|| fail "daemon $suffix admin socket was not created"
	local hello="$E2E_RUNTIME/daemon-$suffix-hello.json"
	retry "$ALMAGEST_E2E_TIMEOUT" "daemon $suffix isolation status" \
		almagest-cli --url "unix://$admin_socket" admin hello --client-name "e2e-$E2E_CASE-$suffix" \
		>"$hello" || fail "daemon $suffix admin API did not answer hello"
	jq -e '.execution_mode == "isolation" and .isolation.protocol_version > 0' "$hello" >/dev/null \
		|| fail "daemon $suffix did not report an active isolation profile"
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
	local actual
	if retry "$ALMAGEST_E2E_TIMEOUT" "node $id label $label" \
		node_label_is "$id" "$label"; then
		return 0
	fi
	actual="$(node_label "$id" 2>/dev/null || true)"
	log "node $id current label: ${actual:-unavailable}"
	return 1
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

wait_static_http_target_ready() {
	local pid="$1"
	local ready_file="$2"
	local error_log="$3"
	local deadline=$((SECONDS + ALMAGEST_E2E_TIMEOUT))
	local status=0

	while ((SECONDS <= deadline)); do
		if [[ -f "$ready_file" ]] && kill -0 "$pid" 2>/dev/null; then
			rm -f "$ready_file"
			return 0
		fi
		if ! kill -0 "$pid" 2>/dev/null; then
			wait "$pid" || status=$?
			if ((status == 0)); then
				status=1
			fi
			log "static HTTP target exited before it was ready"
			if [[ -s "$error_log" ]]; then
				log "static HTTP target stderr follows"
				cat "$error_log" >&2
			fi
			rm -f "$ready_file"
			return "$status"
		fi
		sleep 0.05
	done

	log "timed out waiting for static HTTP target"
	stop_static_http_target "$pid"
	rm -f "$ready_file"
	return 1
}

start_static_http_target() {
	local host="$1"
	local port="$2"
	local body="$3"
	local access_log="$4"
	local ready_file="${access_log}.ready"
	local pid

	rm -f "$access_log" "$access_log.stderr" "$ready_file"
	python3 -u - "$host" "$port" "$body" "$access_log" "$ready_file" <<'PY' >/dev/null 2>"$access_log.stderr" &
import http.server
import socket
import socketserver
import sys

host = sys.argv[1]
port = int(sys.argv[2])
body = sys.argv[3].encode()
access_log = sys.argv[4]
ready_file = sys.argv[5]


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


class Server(http.server.ThreadingHTTPServer):
    address_family = socket.AF_INET6 if ":" in host else socket.AF_INET

    def server_bind(self):
        # HTTPServer does an unused reverse lookup that can block isolated fixtures.
        socketserver.TCPServer.server_bind(self)
        self.server_name = host
        self.server_port = self.server_address[1]


server = Server((host, port), Handler)
with open(ready_file, "x", encoding="utf-8"):
    pass
server.serve_forever()
PY
	pid="$!"
	wait_static_http_target_ready "$pid" "$ready_file" "$access_log.stderr" || return
	echo "$pid"
}

start_static_http_target_in_netns() {
	local netns="$1"
	local host="$2"
	local port="$3"
	local body="$4"
	local access_log="$5"
	local ready_file="${access_log}.ready"
	local pid

	rm -f "$access_log" "$access_log.stderr" "$ready_file"
	ip netns exec "$netns" python3 -u -c '
import http.server
import socket
import socketserver
import sys

host = sys.argv[1]
port = int(sys.argv[2])
body = sys.argv[3].encode()
access_log = sys.argv[4]
ready_file = sys.argv[5]


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


class Server(http.server.ThreadingHTTPServer):
    address_family = socket.AF_INET6 if ":" in host else socket.AF_INET

    def server_bind(self):
        # HTTPServer does an unused reverse lookup that can block isolated fixtures.
        socketserver.TCPServer.server_bind(self)
        self.server_name = host
        self.server_port = self.server_address[1]


server = Server((host, port), Handler)
with open(ready_file, "x", encoding="utf-8"):
    pass
server.serve_forever()
' "$host" "$port" "$body" "$access_log" "$ready_file" >/dev/null 2>"$access_log.stderr" &
	pid="$!"
	wait_static_http_target_ready "$pid" "$ready_file" "$access_log.stderr" || return
	echo "$pid"
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
