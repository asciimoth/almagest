#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

failure_index=0

expect_startup_failure() {
	local expected="$1"
	shift
	local log_file="$E2E_RUNTIME/failure-$failure_index.log"
	((failure_index += 1))
	if XDG_RUNTIME_DIR="$E2E_RUNTIME" timeout 15 "$@" >"$log_file" 2>&1; then
		fail "startup unexpectedly succeeded: $*"
	fi
	grep -F -- "$expected" "$log_file" >/dev/null \
		|| fail "startup failure did not report '$expected': $*"
}

expect_startup_failure "--experimental requires explicit --no-isolation" \
	almagestd --experimental --file-storage "$E2E_RUNTIME/rejected-experimental-store" \
	--unix "$E2E_RUNTIME/rejected-experimental.sock" --web 0
expect_startup_failure "isolation is not implemented yet" \
	almagestd --file-storage "$E2E_RUNTIME/rejected-isolation-store" \
	--unix "$E2E_RUNTIME/rejected-isolation.sock" --web 0
[[ ! -e "$E2E_RUNTIME/rejected-experimental-store" \
	&& ! -e "$E2E_RUNTIME/rejected-experimental.sock" \
	&& ! -e "$E2E_RUNTIME/rejected-isolation-store" \
	&& ! -e "$E2E_RUNTIME/rejected-isolation.sock" ]] \
	|| fail "rejected execution modes mutated storage or created an admin socket"

base=(almagestd --experimental --no-isolation -c "$E2E_CONFIG_DIR/alm.hujson" -p "$E2E_CONFIG_DIR/protected_alm.hujson" \
	--unsafe-config --tmp-file-storage --unix "" --web 0)

expect_startup_failure "--stdin and --snapshot -" \
	almagestd --experimental --no-isolation --stdin --snapshot - --tmp-file-storage --unix "" --web 0
expect_startup_failure "unknown argument" almagestd --experimental --no-isolation --not-an-almagest-option
expect_startup_failure "invalid syntax" env ALMAGEST_E2E_SYSTEM_RANDOM_SEED=invalid "${base[@]}"

printf '%s\n' '{' >"$E2E_RUNTIME/bad-main.hujson"
expect_startup_failure "config parsing" almagestd --experimental --no-isolation -c "$E2E_RUNTIME/bad-main.hujson" \
	-p "$E2E_CONFIG_DIR/protected_alm.hujson" --unsafe-config --tmp-file-storage --unix "" --web 0
printf '%s\n' '{' >"$E2E_RUNTIME/bad-protected.hujson"
expect_startup_failure "protected config parsing" almagestd --experimental --no-isolation -c "$E2E_CONFIG_DIR/alm.hujson" \
	-p "$E2E_RUNTIME/bad-protected.hujson" --unsafe-config --tmp-file-storage --unix "" --web 0

storage_file=/var/lib/almagest-e2e-startup-validation-storage-file
printf '%s\n' storage-is-a-file >"$storage_file"
expect_startup_failure "not a directory" almagestd --experimental --no-isolation -c "$E2E_CONFIG_DIR/alm.hujson" \
	-p "$E2E_CONFIG_DIR/protected_alm.hujson" --unsafe-config \
	--file-storage "$storage_file" --unix "" --web 0
expect_startup_failure "open startup snapshot archive" "${base[@]}" \
	--snapshot "$E2E_RUNTIME/missing-snapshot.zip"
printf '%s\n' not-a-zip >"$E2E_RUNTIME/not-a-snapshot.zip"
expect_startup_failure "snapshot is invalid" "${base[@]}" \
	--snapshot "$E2E_RUNTIME/not-a-snapshot.zip"

write_protected() {
	printf '%s\n' "$1" >"$E2E_RUNTIME/candidate-protected.hujson"
}

write_protected '{"Secrets":{"":{"Value":"x"}},"Admin":{"Web":0,"Unix":""}}'
expect_startup_failure "invalid protected secret key" almagestd --experimental --no-isolation -c "$E2E_CONFIG_DIR/alm.hujson" \
	-p "$E2E_RUNTIME/candidate-protected.hujson" --unsafe-config --tmp-file-storage
write_protected '{"Secrets":{"missing":{}},"Admin":{"Web":0,"Unix":""}}'
expect_startup_failure "must set Value or Env" almagestd --experimental --no-isolation -c "$E2E_CONFIG_DIR/alm.hujson" \
	-p "$E2E_RUNTIME/candidate-protected.hujson" --unsafe-config --tmp-file-storage
write_protected '{"Secrets":{"both":{"Value":"x","Env":"PATH"}},"Admin":{"Web":0,"Unix":""}}'
expect_startup_failure "sets both Value and Env" almagestd --experimental --no-isolation -c "$E2E_CONFIG_DIR/alm.hujson" \
	-p "$E2E_RUNTIME/candidate-protected.hujson" --unsafe-config --tmp-file-storage
write_protected '{"Secrets":{"environment":{"Env":"ALMAGEST_E2E_MISSING_ENV"}},"Admin":{"Web":0,"Unix":""}}'
expect_startup_failure "is not set" env -u ALMAGEST_E2E_MISSING_ENV almagestd --experimental --no-isolation \
	-c "$E2E_CONFIG_DIR/alm.hujson" -p "$E2E_RUNTIME/candidate-protected.hujson" \
	--unsafe-config --tmp-file-storage
write_protected '{"Secrets":{"node":{"Value":"x","Nodes":[""]}},"Admin":{"Web":0,"Unix":""}}'
expect_startup_failure "invalid protected secret node" almagestd --experimental --no-isolation -c "$E2E_CONFIG_DIR/alm.hujson" \
	-p "$E2E_RUNTIME/candidate-protected.hujson" --unsafe-config --tmp-file-storage

write_protected '{"TLS":{"Certificates":{"os-ca:reserved":{"PEM":"x"}}},"Admin":{"Web":0,"Unix":""}}'
expect_startup_failure "invalid protected TLS certificate name" almagestd --experimental --no-isolation -c "$E2E_CONFIG_DIR/alm.hujson" \
	-p "$E2E_RUNTIME/candidate-protected.hujson" --unsafe-config --tmp-file-storage
write_protected '{"TLS":{"Certificates":{"missing":{}}},"Admin":{"Web":0,"Unix":""}}'
expect_startup_failure "must set PEM or File" almagestd --experimental --no-isolation -c "$E2E_CONFIG_DIR/alm.hujson" \
	-p "$E2E_RUNTIME/candidate-protected.hujson" --unsafe-config --tmp-file-storage
write_protected '{"TLS":{"Certificates":{"both":{"PEM":"x","File":"missing"}}},"Admin":{"Web":0,"Unix":""}}'
expect_startup_failure "sets both PEM and File" almagestd --experimental --no-isolation -c "$E2E_CONFIG_DIR/alm.hujson" \
	-p "$E2E_RUNTIME/candidate-protected.hujson" --unsafe-config --tmp-file-storage
write_protected '{"TLS":{"Certificates":{"missing":{"File":"/missing.pem"}}},"Admin":{"Web":0,"Unix":""}}'
expect_startup_failure "read protected TLS certificate" almagestd --experimental --no-isolation -c "$E2E_CONFIG_DIR/alm.hujson" \
	-p "$E2E_RUNTIME/candidate-protected.hujson" --unsafe-config --tmp-file-storage

printf '%s\n' '{"Config":{"TLS":{"BuiltinCA":{"KeyAlgorithm":"bad"}}},"nodes":{}}' \
	>"$E2E_RUNTIME/bad-builtin.hujson"
expect_startup_failure "unsupported builtin TLS CA key algorithm" almagestd --experimental --no-isolation -c "$E2E_RUNTIME/bad-builtin.hujson" \
	-p "$E2E_CONFIG_DIR/protected_alm.hujson" --unsafe-config --tmp-file-storage --unix "" --web 0

write_protected '{"CommandRunner":{"Commands":{"bad":{"Command":[]}}},"Admin":{"Web":0,"Unix":""}}'
expect_startup_failure "empty argv" almagestd --experimental --no-isolation -c "$E2E_CONFIG_DIR/alm.hujson" \
	-p "$E2E_RUNTIME/candidate-protected.hujson" --unsafe-config --tmp-file-storage
write_protected '{"System":{"Type":"invalid"},"Admin":{"Web":0,"Unix":""}}'
expect_startup_failure "system integration type" almagestd --experimental --no-isolation -c "$E2E_CONFIG_DIR/alm.hujson" \
	-p "$E2E_RUNTIME/candidate-protected.hujson" --unsafe-config --tmp-file-storage
write_protected '{"System":{"Type":"debug","Debug":{"DefaultTun":{"Builder":"invalid"}}},"Admin":{"Web":0,"Unix":""}}'
expect_startup_failure "system debug default tun builder" almagestd --experimental --no-isolation -c "$E2E_CONFIG_DIR/alm.hujson" \
	-p "$E2E_RUNTIME/candidate-protected.hujson" --unsafe-config --tmp-file-storage

start_daemon
cli admin ping --nonce startup-validation | jq -e '.nonce == "startup-validation"' >/dev/null \
	|| fail "daemon did not start after rejected startup inputs"
log "case passed"
