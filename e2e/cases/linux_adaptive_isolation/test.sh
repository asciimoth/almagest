#!/usr/bin/env bash
set -euo pipefail

# shellcheck disable=SC1091 # The path is fixed in the E2E image.
source /e2e/lib/common.sh

account=almagest-auto-e2e
uid=20000
gid=20000
home=/var/lib/almagest-auto-e2e
user_runtime=/run/user/20000
case_runtime="$E2E_RUNTIME/adaptive"
direct_config="$case_runtime/direct-config"
active_pid=""
active_log=""
active_socket=""

install -d -o "$uid" -g "$gid" -m 0700 "$case_runtime" "$direct_config"
install -o "$uid" -g "$gid" -m 0600 \
	"$E2E_CONFIG_DIR/alm.hujson" "$direct_config/alm.hujson"
install -o "$uid" -g "$gid" -m 0600 \
	"$E2E_CONFIG_DIR/protected_alm.hujson" "$direct_config/protected_alm.hujson"

cleanup_adaptive() {
	local status="$?"
	trap - EXIT INT TERM
	stop_adaptive
	exit "$status"
}
trap cleanup_adaptive EXIT
trap 'exit 130' INT TERM

stop_adaptive() {
	if [[ -z "$active_pid" ]]; then
		return
	fi
	if kill -0 "$active_pid" 2>/dev/null; then
		kill "$active_pid" 2>/dev/null || true
		local deadline=$((SECONDS + 10))
		while kill -0 "$active_pid" 2>/dev/null && ((SECONDS <= deadline)); do
			sleep 0.25
		done
		if kill -0 "$active_pid" 2>/dev/null; then
			kill -KILL "$active_pid" 2>/dev/null || true
		fi
	fi
	wait "$active_pid" 2>/dev/null || true
	active_pid=""
}

wait_for_admin() {
	local socket="$1"
	local deadline=$((SECONDS + 30))
	while ((SECONDS <= deadline)); do
		if [[ -S "$socket" ]] && timeout 5 almagest-cli --url "unix://$socket" \
			admin ping --nonce adaptive >/dev/null 2>&1; then
			return 0
		fi
		if ! kill -0 "$active_pid" 2>/dev/null; then
			return 1
		fi
		sleep 0.25
	done
	return 1
}

start_adaptive() {
	local name="$1"
	local capability_mode="$2"
	shift 2
	local socket="$case_runtime/$name.sock"
	local -a admin_args=(--unix "$socket")
	if [[ "${1:-}" == "@default-socket" ]]; then
		shift
		local state_root="$home/.local/share/almagest/isolation"
		local state_digest
		state_digest="$(printf '%s' "$state_root" | sha256sum | cut -c1-16)"
		socket="$user_runtime/almagest-$state_digest/admin.sock"
		admin_args=()
	fi
	active_log="$case_runtime/$name.log"
	active_socket="$socket"
	local -a privileges=(
		--reuid="$uid" --regid="$gid" --clear-groups
		--bounding-set=-all --inh-caps=-all --ambient-caps=-all
	)
	if [[ "$capability_mode" == "net-admin" ]]; then
		privileges=(
			--reuid="$uid" --regid="$gid" --clear-groups
			'--bounding-set=-all,+net_admin'
			--inh-caps=+net_admin --ambient-caps=+net_admin
		)
	fi
	rm -f "$socket"
	: >"$active_log"
	setpriv "${privileges[@]}" env \
		HOME="$home" USER="$account" LOGNAME="$account" XDG_RUNTIME_DIR="$user_runtime" \
		almagestd "$@" "${admin_args[@]}" --web 0 >"$active_log" 2>&1 &
	active_pid="$!"
	if ! wait_for_admin "$socket"; then
		tail -200 "$active_log" >&2 || true
		fail "$name daemon did not start"
	fi
	timeout 10 almagest-cli --url "unix://$socket" admin hello \
		--client-name "adaptive-$name" >"$case_runtime/$name-hello.json"
}

adaptive_cli() {
	almagest-cli --url "unix://$active_socket" "$@"
}

expect_failure() {
	local name="$1"
	local expected="$2"
	shift 2
	local log_file="$case_runtime/failure-$name.log"
	local status=0
	set +e
	timeout 15 setpriv \
		--reuid="$uid" --regid="$gid" --clear-groups \
		--bounding-set=-all --inh-caps=-all --ambient-caps=-all \
		env HOME="$home" USER="$account" LOGNAME="$account" XDG_RUNTIME_DIR="$user_runtime" \
		almagestd "$@" --unix "" --web 0 >"$log_file" 2>&1
	status=$?
	set -e
	if ((status == 0)); then
		fail "$name unexpectedly succeeded"
	fi
	if ((status == 124)); then
		fail "$name did not reject startup before the timeout"
	fi
	grep -F -- "$expected" "$log_file" >/dev/null \
		|| fail "$name did not report: $expected"
}

assert_reduced_status() {
	local hello="$1"
	jq -e --argjson uid "$uid" --argjson gid "$gid" '
		.execution_mode == "isolation" and
		.isolation.requested_policy == "auto" and
		.isolation.status_schema_version == 1 and
		.isolation.profile_id == "linux-user-v1" and
		.isolation.profile_revision == 1 and
		.isolation.verification_state == "verified" and
		.isolation.reduced == true and
		.isolation.recovery_state == "complete" and
		.isolation.credentials.network.real_uid == $uid and
		.isolation.credentials.network.real_gid == $gid and
		.isolation.credentials.runtime.effective_uid == $uid and
		.isolation.roles.config_writer == "ready" and
		.isolation.credentials.config_writer.filesystem_uid == $uid and
		.isolation.mechanisms.runtime.landlock.state == "applied" and
		.isolation.mechanisms.runtime.seccomp.state == "applied" and
		.isolation.mechanisms.runtime."no-new-privileges".state == "applied" and
		.isolation.mechanisms.runtime."role-cgroups" == {"state":"unavailable","reason":"not-delegated"} and
		.isolation.mechanisms.runtime."private-namespaces" == {"state":"unavailable","reason":"profile-not-implemented"} and
		.isolation.mechanisms.runtime."separate-host-identities" == {"state":"unavailable","reason":"same-uid-profile"} and
		.isolation.mechanisms.config_writer."fixed-target".state == "applied" and
		.isolation.capability_sets.config_writer.inheritable == [] and
		.isolation.capability_sets.config_writer.permitted == [] and
		.isolation.capability_sets.config_writer.effective == [] and
		.isolation.capability_sets.config_writer.ambient == [] and
		([.isolation.warnings[].code] | sort) == ["not-delegated","profile-not-implemented","same-uid-profile"] and
		([.isolation.warnings[] | select(.role != "runtime" or (.effect | length) == 0 or (.feature | length) == 0)] | length) == 0 and
		([.isolation.resource_limits.runtime[] | select(.scope == "role") | .enforcement] | unique) == ["unavailable"] and
		(.isolation.selected_plan_digest | length) == 64 and
		(.isolation.observation_time | length) > 0
	' "$hello" >/dev/null || fail "reduced isolation status is incomplete"
}

assert_warnings() {
	local log_file="$1"
	grep -F 'INFO isolation policy=auto profile=linux-user-v1 state=selected reduced=true' "$log_file" >/dev/null
	grep -F 'INFO isolation policy=auto profile=linux-user-v1 state=verified reduced=true' "$log_file" >/dev/null
	grep -F 'WARN isolation role=runtime reason=profile-not-implemented effect=' "$log_file" >/dev/null
	grep -F 'WARN isolation role=runtime reason=not-delegated effect=' "$log_file" >/dev/null
	grep -F 'WARN isolation role=runtime reason=same-uid-profile effect=' "$log_file" >/dev/null
}

# Linux defaults to automatic selection. Drop every capability, including the
# bounding set, before starting the ordinary-user broker.
start_adaptive default-auto none
assert_reduced_status "$case_runtime/default-auto-hello.json"
jq -e '
	[.isolation.capability_sets.network[], .isolation.capability_sets.runtime[]]
	| all(length == 0)
' "$case_runtime/default-auto-hello.json" >/dev/null \
	|| fail "no-capability launch reported a nonempty capability set"
assert_warnings "$active_log"
adaptive_cli config snapshot >"$case_runtime/user-config.json"
config_revision="$(jq -er '.revision' "$case_runtime/user-config.json")"
cat >"$case_runtime/user-updated.hujson" <<'EOF'
{"nodes":{"adaptive_qualification":{"Class":"almagest/MockNetwork"}}}
EOF
adaptive_cli config apply --revision "$config_revision" "$case_runtime/user-updated.hujson" \
	>"$case_runtime/user-config-result.json"
adaptive_cli graph node --name adaptive_qualification >/dev/null \
	|| fail "user-profile config writer did not persist an update"
adaptive_cli snapshots create --name adaptive-user >"$case_runtime/user-snapshot.json"
snapshot_id="$(jq -er '.uuid' "$case_runtime/user-snapshot.json")"
adaptive_cli snapshots restore "$snapshot_id" >"$case_runtime/user-restore.json"
adaptive_cli graph node --name adaptive_qualification >/dev/null \
	|| fail "user-profile snapshot restore lost the configured node"
stop_adaptive

# The user profile supplies a private default socket when no override is set.
start_adaptive default-socket none @default-socket
assert_reduced_status "$case_runtime/default-socket-hello.json"
adaptive_cli graph node --name adaptive_qualification >/dev/null \
	|| fail "user-profile state did not survive restart after snapshot restore"
stop_adaptive

# Exercise the service-manager style capability handoff when util-linux knows
# the requested capability and can construct an ambient set.
if setpriv --list-caps | grep -Fx net_admin >/dev/null; then
	ambient_state="$home/ambient-state"
	install -d -o "$uid" -g "$gid" -m 0700 "$ambient_state"
	start_adaptive ambient-net-admin net-admin \
		--isolation=auto --isolation-state-dir "$ambient_state"
	assert_reduced_status "$case_runtime/ambient-net-admin-hello.json"
	jq -e '
		.isolation.capability_sets.network.inheritable == ["CAP_NET_ADMIN"] and
		.isolation.capability_sets.network.permitted == ["CAP_NET_ADMIN"] and
		.isolation.capability_sets.network.effective == ["CAP_NET_ADMIN"] and
		.isolation.capability_sets.network.bounding == ["CAP_NET_ADMIN"] and
		.isolation.capability_sets.network.ambient == ["CAP_NET_ADMIN"] and
		.isolation.capability_sets.runtime.inheritable == [] and
		.isolation.capability_sets.runtime.permitted == [] and
		.isolation.capability_sets.runtime.effective == [] and
		.isolation.capability_sets.runtime.bounding == ["CAP_NET_ADMIN"] and
		.isolation.capability_sets.runtime.ambient == []
	' "$case_runtime/ambient-net-admin-hello.json" >/dev/null \
		|| fail "ambient NET_ADMIN handoff was not reported accurately"
	assert_warnings "$active_log"
	stop_adaptive
else
	log "setpriv does not list net_admin; ambient capability launch skipped"
fi

expect_failure strict-nonroot 'linux-full-v1 prerequisites are unavailable' \
	--isolation=strict
expect_failure conflicting-alias '--no-isolation conflicts with --isolation=auto' \
	--no-isolation --isolation=auto
expect_failure unknown-policy 'invalid --isolation value "adaptive"' \
	--isolation=adaptive
expect_failure implicit-state-root '--isolation-state-dir requires explicit --isolation=auto' \
	--isolation-state-dir "$home/implicit-state"
expect_failure unavailable-cgroups 'explicit memory, task, or CPU limits require per-role cgroups' \
	--isolation=auto --isolation-memory-bytes 33554432
expect_failure isolated-temporary-paths 'temporary storage and admin paths are not qualified' \
	--isolation=auto --tmp-file-storage

start_adaptive explicit-off none \
	--isolation=off --unsafe-config --tmp-file-storage \
	-c "$direct_config/alm.hujson" -p "$direct_config/protected_alm.hujson"
jq -e '.execution_mode == "direct" and .isolation == null' \
	"$case_runtime/explicit-off-hello.json" >/dev/null \
	|| fail "explicit off did not select direct mode"
grep -F 'Daemon process isolation is disabled' "$active_log" >/dev/null \
	|| fail "explicit off did not report the direct-mode warning"
stop_adaptive

start_adaptive no-isolation-alias none \
	--no-isolation --unsafe-config --tmp-file-storage \
	-c "$direct_config/alm.hujson" -p "$direct_config/protected_alm.hujson"
jq -e '.execution_mode == "direct" and .isolation == null' \
	"$case_runtime/no-isolation-alias-hello.json" >/dev/null \
	|| fail "no-isolation alias did not select direct mode"
stop_adaptive

start_adaptive consistent-off-alias none \
	--no-isolation --isolation=off --unsafe-config --tmp-file-storage \
	-c "$direct_config/alm.hujson" -p "$direct_config/protected_alm.hujson"
jq -e '.execution_mode == "direct" and .isolation == null' \
	"$case_runtime/consistent-off-alias-hello.json" >/dev/null \
	|| fail "consistent direct aliases did not select direct mode"
stop_adaptive

log "case passed"
