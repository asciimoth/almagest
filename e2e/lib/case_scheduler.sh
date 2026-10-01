#!/usr/bin/env bash

e2e_case_is_exclusive() {
	local case_dir="${1:?case directory is required}"
	[[ -f "$case_dir/exclusive" ]]
}

e2e_case_can_start() {
	local case_dir="${1:?case directory is required}"
	local running="${2:?running case count is required}"
	local exclusive_running="${3:?exclusive state is required}"

	if ((exclusive_running)); then
		return 1
	fi
	if e2e_case_is_exclusive "$case_dir" && ((running > 0)); then
		return 1
	fi
	return 0
}

e2e_add_case_privileges() {
	local case_dir="${1:?case directory is required}"
	local args_name="${2:?argument array name is required}"
	local -n args_ref="$args_name"

	if [[ -f "$case_dir/requires-privileged" ]]; then
		args_ref+=(--privileged --device /dev/net/tun)
	fi
}

e2e_shuffle_cases() {
	local seed="${1:?random seed is required}"
	local cases_name="${2:?case array name is required}"
	local state="$seed"
	local index
	local swap_index
	local value
	local -n cases_ref="$cases_name"

	# Use a local generator so the same seed gives the same order regardless of
	# other uses of Bash's RANDOM variable.
	for ((index = ${#cases_ref[@]} - 1; index > 0; index--)); do
		state=$(((1103515245 * state + 12345) & 2147483647))
		swap_index=$((state % (index + 1)))
		value="${cases_ref[$index]}"
		cases_ref[index]="${cases_ref[$swap_index]}"
		cases_ref[swap_index]="$value"
	done
}
