#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
repo_uid=$(stat -c '%u' -- "$repo_root")

if [[ $EUID != "$repo_uid" ]]; then
	printf 'refusing to run repository recipe as uid %s; checkout owner is uid %s\n' \
		"$EUID" "$repo_uid" >&2
	printf '%s\n' 'run just without sudo; recipes that need privilege elevate only their runtime command' >&2
	exit 1
fi

exec "$@"
