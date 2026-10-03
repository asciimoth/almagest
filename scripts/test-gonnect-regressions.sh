#!/usr/bin/env bash
set -euo pipefail

upstream_module="github.com/asciimoth/gonnect"

tests=(
	TestLoopbackTCPListenerRejectsConnectionAfterClose
	TestLoopbackTCPListenerStaleLookupCannotReachReplacement
	TestLoopbackTCPListenerCloseDrainsQueuedConnections
	TestLoopbackTCPListenerAcceptRejectsQueuedConnectionAfterClose
	TestLoopbackTCPListenerCloseWakesBlockedAccept
	TestLoopbackTCPListenerCloseWakesBlockedNewConn
	TestLoopbackTCPListenerConcurrentCloseAndNewConn
)

listed="$(go -C lib test "$upstream_module" -list '^TestLoopbackTCPListener')"
for test_name in "${tests[@]}"; do
	if ! grep -Fxq "$test_name" <<<"$listed"; then
		printf 'required %s regression test is missing: %s\n' "$upstream_module" "$test_name" >&2
		exit 1
	fi
done

pattern="$(IFS='|'; printf '%s' "${tests[*]}")"
go -C lib test "$upstream_module" -run "^(${pattern})$" -count=10

go -C lib test ./http \
	-run '^TestHTTPServerThrough(SocksClientE2E|SocksClientRecoversFromRepeatedSocksListenerRestarts|PlainSocksBindDoesNotStopBeforeFirstAccept)$' \
	-count=3 \
	-timeout=3m
