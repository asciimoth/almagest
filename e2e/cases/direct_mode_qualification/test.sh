#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

# common.sh owns this process ID. This assignment makes the shared contract
# visible when shellcheck checks this case without following imports.
daemon_pid=""

assert_no_role_children() {
	local children_file
	local children
	for children_file in /proc/"$daemon_pid"/task/*/children; do
		children="$(<"$children_file")"
		[[ -z "$children" ]] || fail "direct daemon task has child processes: $children"
	done
}

assert_only_public_admin_socket() {
	local expected_inode
	local target
	local inode
	local unexpected=()
	expected_inode="$(awk -v path="$E2E_ADMIN_SOCK" '$8 == path {gsub(":", "", $7); print $7}' /proc/net/unix)"
	[[ -n "$expected_inode" ]] || fail "public admin socket inode was not found"
	while IFS= read -r target; do
		[[ "$target" =~ ^socket:\[([0-9]+)\]$ ]] || continue
		inode="${BASH_REMATCH[1]}"
		if [[ "$inode" != "$expected_inode" ]]; then
			unexpected+=("$inode")
		fi
	done < <(find /proc/"$daemon_pid"/fd -maxdepth 1 -type l -printf '%l\n' 2>/dev/null || true)
	((${#unexpected[@]} == 0)) \
		|| fail "direct daemon has private or non-admin socket inodes: ${unexpected[*]}"
}

assert_direct_boundary() {
	assert_no_role_children
	assert_only_public_admin_socket
}

start_daemon
assert_daemon_log_contains "Execution mode: direct (no sandbox)"
assert_direct_boundary

cli config snapshot > /tmp/direct-mode-config.json
revision="$(jq -er '.revision' /tmp/direct-mode-config.json)"
cat > /tmp/direct-mode-updated.hujson <<'EOF'
{"nodes":{"qualification":{"Class":"almagest/MockNetwork"}}}
EOF
cli config apply --revision "$revision" /tmp/direct-mode-updated.hujson >/tmp/direct-mode-config-result.json
wait_graph_node qualification || fail "graph replacement did not create the qualification node"
assert_direct_boundary

cli snapshots create --name direct-mode >/tmp/direct-mode-snapshot.json
snapshot_id="$(jq -er '.uuid' /tmp/direct-mode-snapshot.json)"
cli snapshots restore "$snapshot_id" >/tmp/direct-mode-restore.json
wait_graph_node qualification || fail "snapshot restore did not retain the qualification node"
assert_direct_boundary

pid="$daemon_pid"
stop_daemon
[[ ! -e "/proc/$pid" ]] || fail "direct daemon process remained after shutdown"
log "case passed"
