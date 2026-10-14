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

start_daemon
cli admin hello --client-name linux-isolation-profile \
	| jq -e '
		.execution_mode == "isolation" and
		.isolation.recovery_state == "complete" and
		.isolation.roles.network == "ready" and
		.isolation.roles.runtime == "ready" and
		.isolation.roles.config_writer == "ready" and
		(.isolation.restrictions.runtime | index("landlock")) != null and
		(.isolation.restrictions.runtime | index("seccomp")) != null
	' >/dev/null
log "case passed"
