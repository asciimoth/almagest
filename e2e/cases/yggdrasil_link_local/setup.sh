#!/usr/bin/env bash
set -euo pipefail
command -v ip >/dev/null
command -v jq >/dev/null
[[ -c /dev/net/tun ]]
if ! ip link show yga0 >/dev/null 2>&1; then
	ip link add yga0 type veth peer name ygb0
fi
ip link set yga0 up
ip link set ygb0 up
ip -6 addr replace fe80::a/64 dev yga0 nodad
ip -6 addr replace fe80::b/64 dev ygb0 nodad
ip -6 addr show dev yga0 scope link
ip -6 addr show dev ygb0 scope link
