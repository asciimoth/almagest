#!/usr/bin/env bash
set -euo pipefail

# shellcheck disable=SC1091 # This script runs from the E2E image.
source /e2e/lib/case_scheduler.sh

case_name="${1:?case name is required}"
source_dir="/e2e/cases/$case_name"

if [[ ! -d "$source_dir" ]]; then
	echo "unknown e2e case: $case_name" >&2
	exit 1
fi

export E2E_CASE="$case_name"
export E2E_CASE_SOURCE="$source_dir"
export E2E_RUNTIME="/run/almagest-e2e/$case_name"
export E2E_ISOLATION_ROOT="$E2E_RUNTIME/isolation"
export E2E_CONFIG_DIR="$E2E_ISOLATION_ROOT/config"
export E2E_ADMIN_SOCK="$E2E_RUNTIME/admin.sock"
export E2E_DAEMON_LOG="$E2E_RUNTIME/daemon.log"
if [[ -f "$source_dir/isolation-mode-matrix" ]]; then
	case_modes=(off auto strict)
elif [[ -f "$source_dir/sandbox-mode-matrix" ]]; then
	case_modes=(off auto strict)
elif [[ -f "$source_dir/requires-direct" ]]; then
	case_modes=(direct)
elif [[ -f "$source_dir/also-direct" ]]; then
	case_modes=(isolation direct)
else
	case_modes=(isolation)
fi

if [[ -f "$source_dir/sandbox-mode-matrix" ]]; then
	sandbox_modes=(off auto strict)
else
	sandbox_modes=("$(e2e_case_default_sandbox_mode "$source_dir")")
fi

if [[ " ${case_modes[*]} " == *" isolation "* || " ${case_modes[*]} " == *" auto "* || " ${case_modes[*]} " == *" strict "* || -f "$source_dir/requires-cgroup" ]]; then
	cgroup_path="$(awk -F: '$1 == "0" {print $3; exit}' /proc/self/cgroup)"
	if [[ "$cgroup_path" != /* ]]; then
		echo "unified cgroup path is unavailable" >&2
		exit 1
	fi
	export E2E_CGROUP_PARENT="/sys/fs/cgroup${cgroup_path%/}"
	runner_cgroup="$E2E_CGROUP_PARENT/almagest-e2e-runner"
	mkdir "$runner_cgroup"
	printf '%s\n' "$BASHPID" >"$runner_cgroup/cgroup.procs"
	printf '%s\n' '+cpu +memory +pids' >"$E2E_CGROUP_PARENT/cgroup.subtree_control"
fi

run_case_mode() (
	local mode="${1:?daemon mode is required}"
	local sandbox_mode="${2:?sandbox mode is required}"
	if [[ "$mode" == "direct" || "$mode" == "off" ]]; then
		export ALMAGEST_E2E_DAEMON_MODE=direct
		export ALMAGEST_E2E_ISOLATION_MODE=off
	else
		export ALMAGEST_E2E_DAEMON_MODE=isolation
		if [[ "$mode" == "isolation" ]]; then
			export ALMAGEST_E2E_ISOLATION_MODE=auto
		else
			export ALMAGEST_E2E_ISOLATION_MODE="$mode"
		fi
	fi
	export ALMAGEST_E2E_SANDBOX_MODE="$sandbox_mode"

	rm -rf "$E2E_RUNTIME"
	mkdir -p "$E2E_CONFIG_DIR"
	cp -a "$source_dir"/. "$E2E_CONFIG_DIR"/
	if [[ -f "$E2E_CONFIG_DIR/protected_alm.hujson" ]]; then
		sed -i "s@%E2E_ADMIN_SOCK%@$E2E_ADMIN_SOCK@g" "$E2E_CONFIG_DIR/protected_alm.hujson"
		if [[ -n "${E2E_CGROUP_PARENT:-}" ]]; then
			sed -i "s@%E2E_CGROUP_PARENT%@$E2E_CGROUP_PARENT@g" "$E2E_CONFIG_DIR/protected_alm.hujson"
		fi
	fi

	cd "$E2E_CONFIG_DIR"
	if [[ -x ./setup.sh ]]; then
		./setup.sh
	elif [[ -f ./setup.sh ]]; then
		bash ./setup.sh
	fi
	mapfile -d '' -t protected_configs < <(find "$E2E_CONFIG_DIR" -name protected_alm.hujson -type f -print0)
	if ((${#protected_configs[@]} == 0)); then
		echo "$case_name has no protected_alm.hujson" >&2
		exit 1
	fi
	python3 /e2e/lib/configure_sandbox_mode.py "$sandbox_mode" "${protected_configs[@]}"
	if [[ ! -x ./test.sh ]]; then
		echo "$case_name/test.sh must exist and be executable" >&2
		exit 1
	fi
	printf '[%s] running isolation=%s sandbox=%s\n' \
		"$case_name" "$ALMAGEST_E2E_ISOLATION_MODE" "$sandbox_mode" >&2
	./test.sh
)

for case_mode in "${case_modes[@]}"; do
	for sandbox_mode in "${sandbox_modes[@]}"; do
		run_case_mode "$case_mode" "$sandbox_mode"
	done
done
