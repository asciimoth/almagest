#!/usr/bin/env bash

e2e_case_is_exclusive() {
	local case_dir="${1:?case directory is required}"
	[[ -f "$case_dir/exclusive" ]]
}

e2e_case_privilege() {
	local case_dir="${1:?case directory is required}"

	if [[ ! -f "$case_dir/requires-direct" ]]; then
		printf 'isolated-privileged\n'
	elif [[ -f "$case_dir/requires-privileged" ]]; then
		printf 'direct-privileged\n'
	else
		printf 'direct\n'
	fi
}

e2e_now_ms() {
	local seconds="${EPOCHREALTIME%.*}"
	local fraction="${EPOCHREALTIME#*.}000"

	printf '%d\n' "$((10#$seconds * 1000 + 10#${fraction:0:3}))"
}

e2e_format_duration() {
	local milliseconds="${1:?duration in milliseconds is required}"
	local hours=$((milliseconds / 3600000))
	local minutes=$(((milliseconds % 3600000) / 60000))
	local seconds=$(((milliseconds % 60000) / 1000))
	local remainder=$((milliseconds % 1000))

	if ((hours > 0)); then
		printf '%dh %02dm %02d.%03ds\n' "$hours" "$minutes" "$seconds" "$remainder"
	elif ((minutes > 0)); then
		printf '%dm %02d.%03ds\n' "$minutes" "$seconds" "$remainder"
	else
		printf '%d.%03ds\n' "$seconds" "$remainder"
	fi
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

e2e_find_startable_case() {
	local cases_root="${1:?cases root is required}"
	local cases_name="${2:?case array name is required}"
	local pending_name="${3:?pending array name is required}"
	local running="${4:?running case count is required}"
	local exclusive_running="${5:?exclusive state is required}"
	local pending_index
	local case_index
	local -n cases_ref="$cases_name"
	local -n pending_ref="$pending_name"

	for pending_index in "${!pending_ref[@]}"; do
		case_index="${pending_ref[$pending_index]}"
		if e2e_case_can_start \
			"$cases_root/${cases_ref[$case_index]}" "$running" "$exclusive_running"; then
			printf '%s\n' "$pending_index"
			return 0
		fi
	done
	return 1
}

e2e_add_case_privileges() {
	local case_dir="${1:?case directory is required}"
	local args_name="${2:?argument array name is required}"
	local -n args_ref="$args_name"

	if [[ -f "$case_dir/requires-privileged" || ! -f "$case_dir/requires-direct" ]]; then
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
