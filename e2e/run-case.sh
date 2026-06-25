#!/usr/bin/env bash
set -euo pipefail

case_name="${1:?case name is required}"
source_dir="/e2e/cases/$case_name"

if [[ ! -d "$source_dir" ]]; then
	echo "unknown e2e case: $case_name" >&2
	exit 1
fi

export E2E_CASE="$case_name"
export E2E_CASE_SOURCE="$source_dir"
export E2E_RUNTIME="/tmp/almagest-e2e/$case_name"
export E2E_CONFIG_DIR="$E2E_RUNTIME/config"
export E2E_ADMIN_SOCK="$E2E_RUNTIME/admin.sock"
export E2E_DAEMON_LOG="$E2E_RUNTIME/daemon.log"

rm -rf "$E2E_RUNTIME"
mkdir -p "$E2E_CONFIG_DIR"
cp -a "$source_dir"/. "$E2E_CONFIG_DIR"/
if [[ -f "$E2E_CONFIG_DIR/protected_alm.hujson" ]]; then
	sed -i "s@%E2E_ADMIN_SOCK%@$E2E_ADMIN_SOCK@g" "$E2E_CONFIG_DIR/protected_alm.hujson"
fi

cd "$E2E_CONFIG_DIR"

if [[ -x ./setup.sh ]]; then
	./setup.sh
elif [[ -f ./setup.sh ]]; then
	bash ./setup.sh
fi

if [[ ! -x ./test.sh ]]; then
	echo "$case_name/test.sh must exist and be executable" >&2
	exit 1
fi

./test.sh
