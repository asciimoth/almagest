#!/bin/sh
set -eu

if command -v systemd-sysusers >/dev/null 2>&1; then
	systemd-sysusers /usr/lib/sysusers.d/almagest.conf
elif ! getent group almagest >/dev/null 2>&1; then
	if ! command -v groupadd >/dev/null 2>&1; then
		echo "cannot create the almagest group: systemd-sysusers and groupadd are unavailable" >&2
		exit 1
	fi
	groupadd --system almagest
fi
if command -v systemd-tmpfiles >/dev/null 2>&1; then
	systemd-tmpfiles --create /usr/lib/tmpfiles.d/almagest.conf
fi
if command -v systemctl >/dev/null 2>&1; then
	systemctl daemon-reload >/dev/null 2>&1 || :
fi
