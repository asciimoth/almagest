#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

profile_cgroup="$E2E_CGROUP_PARENT/almagest-e2e-profile-test"
mkdir "$profile_cgroup"
printf '%s\n' 536870912 >"$profile_cgroup/memory.max"
printf '%s\n' 0 >"$profile_cgroup/memory.swap.max"
printf '%s\n' 128 >"$profile_cgroup/pids.max"
printf '%s\n' '100000 100000' >"$profile_cgroup/cpu.max"

(
	printf '%s\n' "$BASHPID" >"$profile_cgroup/cgroup.procs"
	export ALMAGEST_PROFILE_TEST_NATIVE=1
	export ALMAGEST_PROFILE_CGROUP="$profile_cgroup"
	exec /qualification/profile.test -test.v -test.timeout=90s \
		-test.run '^(TestNativeRoles|TestNativeRejectsUnsafeInputs|TestNativeELFRejection|TestNativeTSyncFailure|TestNativeAlternateABI)$'
)
rmdir "$profile_cgroup"

runuser -u nobody -- env ALMAGEST_PROFILE_TEST_NATIVE=1 \
	/qualification/profile.test -test.v -test.timeout=90s -test.run '^TestNativeUserRoles$'

export ALMAGEST_ISOLATION_MEMORY_BYTES=1073741824
export ALMAGEST_ISOLATION_TASKS=256
export ALMAGEST_ISOLATION_CPU_QUOTA=200000
export ALMAGEST_ISOLATION_DESCRIPTORS=2048
export ALMAGEST_ISOLATION_FILE_BYTES=536870912
start_daemon
for role in network runtime writer sandbox; do
	groups=("${daemon_cgroup:?daemon cgroup is required}"/almagest-instance-*/"$role")
	[[ ${#groups[@]} == 1 && -d "${groups[0]}" ]] || fail "missing $role cgroup"
	group=${groups[0]}
	[[ $(<"$group/memory.max") == 1073741824 ]] || fail "$role memory limit"
	[[ $(<"$group/pids.max") == 256 ]] || fail "$role task limit"
	[[ $(<"$group/cpu.max") == '200000 100000' ]] || fail "$role CPU limit"
	[[ $(<"$group/memory.swap.max") == 0 ]] || fail "$role swap limit"
	# Restricted cgroups also contain the waiting native launcher parent.
	# Find the role process with both requested hard limits.
	role_limits_ok=0
	while read -r role_pid; do
		if awk '$1 == "Max" && $2 == "open" && $3 == "files" && $4 == 2048 && $5 == 2048 { descriptors = 1 }
			$1 == "Max" && $2 == "file" && $3 == "size" && $4 == 536870912 && $5 == 536870912 { file = 1 }
			END { exit !(descriptors && file) }' "/proc/$role_pid/limits"; then
			role_limits_ok=1
		fi
	done <"$group/cgroup.procs"
	[[ $role_limits_ok == 1 ]] || fail "$role process limits"
done
cli admin hello --client-name linux-isolation-profile \
	| jq -e '
		.execution_mode == "isolation" and
		.isolation.recovery_state == "complete" and
		.isolation.roles.network == "ready" and
		.isolation.roles.runtime == "ready" and
		.isolation.roles.config_writer == "ready" and
		.isolation.roles.sandbox_supervisor == "ready" and
		.isolation.capabilities.subprogram_sandboxes and
		.subprogram_sandbox.mode == "strict" and
		.subprogram_sandbox.templates.qualification.profile == "linux-full-v1" and
		.subprogram_sandbox.templates.qualification.strict_satisfied and
		.subprogram_sandbox.templates.qualification.full_boundary and
		.subprogram_sandbox.templates.qualification.effective_identity == "65534:65534" and
		(.isolation.restrictions.runtime | index("landlock")) != null and
		(.isolation.restrictions.runtime | index("seccomp")) != null
	' >/dev/null
log "case passed"
