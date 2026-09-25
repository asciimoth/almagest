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
