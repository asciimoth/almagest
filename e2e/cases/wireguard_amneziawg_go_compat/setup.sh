#!/usr/bin/env bash
set -euo pipefail

command -v amneziawg-go >/dev/null
command -v ip >/dev/null
command -v jq >/dev/null
command -v python3 >/dev/null
command -v socat >/dev/null

if [[ ! -c /dev/net/tun ]]; then
	echo "/dev/net/tun is required; run this case with Docker --privileged" >&2
	exit 1
fi

ip addr show lo >/dev/null

if [[ -f /usr/local/share/amneziawg-go.commit ]]; then
	printf 'amneziawg-go commit: %s\n' "$(cat /usr/local/share/amneziawg-go.commit)" >&2
fi
