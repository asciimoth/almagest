#!/usr/bin/env bash
set -euo pipefail

module="github.com/asciimoth/gonnect-netstack"

assert_test_exists() {
	local package="$1"
	local test_name="$2"
	local listed
	listed="$(go -C lib test "$module/$package" -list "^${test_name}$")"
	if ! grep -Fxq "$test_name" <<<"$listed"; then
		printf 'required %s regression test is missing: %s\n' "$module/$package" "$test_name" >&2
		exit 1
	fi
}

spoofer_tests=(
	TestSetupTCPOptionsDisablesTimeWait
)
vtun_tests=(
	TestSpooferAcceptsReusedClientTupleAfterServerClose
	TestSpooferRepeatedlyReusesClientTuple
	TestSpooferReusesManyClientTuplesConcurrently
	TestSpooferReusesClientTupleAfterReset
	TestSpooferShutdownWhileReusedTupleIsBeingPrepared
)

for test_name in "${spoofer_tests[@]}"; do
	assert_test_exists spoofer "$test_name"
done
for test_name in "${vtun_tests[@]}"; do
	assert_test_exists vtun "$test_name"
done

go -C lib test "$module/spoofer" \
	-run '^TestSetupTCPOptionsDisablesTimeWait$' \
	-count=1
go -C lib test "$module/vtun" \
	-run '^TestSpoofer(AcceptsReusedClientTupleAfterServerClose|RepeatedlyReusesClientTuple|ReusesManyClientTuplesConcurrently|ReusesClientTupleAfterReset|ShutdownWhileReusedTupleIsBeingPrepared)$' \
	-count=1
