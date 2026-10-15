#!/usr/bin/env bash
set -euo pipefail

account=almagest-auto-e2e
uid=20000
gid=20000
home=/var/lib/almagest-auto-e2e
runtime=/run/user/20000

getent group "$account" >/dev/null || groupadd --gid "$gid" "$account"
getent passwd "$account" >/dev/null \
	|| useradd --uid "$uid" --gid "$gid" --home-dir "$home" --create-home "$account"

install -d -o "$uid" -g "$gid" -m 0700 "$home" "$runtime"
test -x /usr/bin/setpriv
test -x /usr/local/bin/almagestd
test -x /usr/local/bin/almagest-network-role
test -x /usr/local/bin/almagest-linux-role-launcher
