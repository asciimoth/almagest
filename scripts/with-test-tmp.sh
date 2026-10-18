#!/usr/bin/env bash
set -euo pipefail

# Honor explicit scratch paths. Large Go build intermediates stay on their
# original filesystem; TMPDIR covers test state, logs, sockets, and profiles.
if [[ ${ALMAGEST_TEST_TMPFS:-1} == 0 || -n ${TMPDIR:-} || $(uname -s) != Linux ]]; then
	exec "$@"
fi
if ! command -v findmnt >/dev/null; then
	exec "$@"
fi

for candidate in "${XDG_RUNTIME_DIR:-/run/user/$(id -u)}" /dev/shm; do
	[[ -d "$candidate" && -w "$candidate" ]] || continue
	[[ $(stat -f -c %T "$candidate") == tmpfs ]] || continue
	# Tests execute helper scripts and binaries from their scratch directories.
	options=$(findmnt -n -o OPTIONS -T "$candidate")
	[[ ,$options, != *,noexec,* ]] || continue
	test_tmp=$(mktemp -d "$candidate/alm-test.XXXXXX")
	trap 'rm -rf -- "$test_tmp"' EXIT
	export GOTMPDIR="${GOTMPDIR:-/tmp}"
	export TMPDIR="$test_tmp"
	"$@"
	exit $?
done

# Hosts without an executable tmpfs retain the normal temporary directory.
exec "$@"
