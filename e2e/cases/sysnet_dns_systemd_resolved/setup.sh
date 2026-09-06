#!/usr/bin/env bash
set -euo pipefail

command -v busctl >/dev/null
command -v dig >/dev/null
command -v dnsmasq >/dev/null
command -v getent >/dev/null
command -v ip >/dev/null
command -v jq >/dev/null
test -c /dev/net/tun

export E2E_DNS_UPSTREAM="%ETH0_IP%"
if [[ "$E2E_DNS_UPSTREAM" == "%ETH0_IP%" ]]; then
	E2E_DNS_UPSTREAM="$(ip -4 -o addr show dev eth0 | awk '{ split($4, a, "/"); print a[1]; exit }')"
fi
printf 'E2E_DNS_UPSTREAM=%q\n' "$E2E_DNS_UPSTREAM" >dns-compat.env
sed -i "s@%E2E_DNS_UPSTREAM%@$E2E_DNS_UPSTREAM@g" alm.hujson protected_alm.hujson
