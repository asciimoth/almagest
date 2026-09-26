#!/usr/bin/env bash
set -euo pipefail

root="$(mktemp -d)"
trap 'rm -rf "$root"' EXIT

export E2E_CASE=common-test
export E2E_RUNTIME="$root/runtime"
export E2E_CONFIG_DIR="$root/config"
export E2E_ADMIN_SOCK="$root/admin.sock"
export E2E_DAEMON_LOG="$root/daemon.log"
unset ALMAGEST_E2E_CLI_TIMEOUT

# shellcheck disable=SC1091 # The path is resolved from this script's directory.
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

if [[ "$ALMAGEST_E2E_CLI_TIMEOUT" != "20" ]]; then
	echo "default CLI timeout = $ALMAGEST_E2E_CLI_TIMEOUT, want 20" >&2
	exit 1
fi

timeout() {
	printf '%s\n' "$*" >"$root/timeout-args"
	return 124
}

if cli config snapshot 2>"$root/cli-error"; then
	echo "timed-out CLI call succeeded" >&2
	exit 1
else
	status=$?
fi
if ((status != 124)); then
	echo "timed-out CLI status = $status, want 124" >&2
	exit 1
fi
if [[ "$(<"$root/timeout-args")" != \
	"--kill-after=2 20 almagest-cli --url unix://$E2E_ADMIN_SOCK config snapshot" ]]; then
	echo "CLI timeout arguments are incorrect: $(<"$root/timeout-args")" >&2
	exit 1
fi
grep -F '[common-test] CLI timed out after 20s: config snapshot' \
	"$root/cli-error" >/dev/null || {
	echo "CLI timeout diagnostic is missing" >&2
	exit 1
}

echo "e2e common helper tests passed"
