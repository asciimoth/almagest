#!/usr/bin/env bash
set -euo pipefail

for command in ip jq python3 sed; do
	command -v "$command" >/dev/null
done

fixture_host="$(ip -4 route get 192.0.2.1 | awk '
	{
		for (field = 1; field <= NF; field++) {
			if ($field == "src") {
				print $(field + 1)
				exit
			}
		}
	}
')"
python3 -c 'import ipaddress, sys; ipaddress.IPv4Address(sys.argv[1])' "$fixture_host"
sed -i "s@%E2E_UDP_REBIND_HOST%@$fixture_host@g" alm.hujson
printf 'fixture_host=%q\n' "$fixture_host" >fixture.env
