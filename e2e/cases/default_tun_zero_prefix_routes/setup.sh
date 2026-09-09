#!/usr/bin/env bash
set -euo pipefail

command -v ip >/dev/null
command -v jq >/dev/null
test -c /dev/net/tun

if ! ip link add e2e-cap-check type dummy 2>/tmp/e2e-default-tun-zero-route-capability.err; then
	echo "default_tun_zero_prefix_routes requires privileged Docker networking" >&2
	echo 'run with E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun"' >&2
	cat /tmp/e2e-default-tun-zero-route-capability.err >&2
	exit 1
fi
ip link del e2e-cap-check
