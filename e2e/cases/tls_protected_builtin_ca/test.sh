#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

write_file_config() {
	local pem_file="$1"
	jq -n --arg file "$pem_file" \
		'{Admin:{Web:0,Unix:"/tmp/almagest-e2e.sock"},TLS:{BuiltinCA:{File:$file}}}' \
		>"$E2E_CONFIG_DIR/protected_alm.hujson"
}

write_inline_config() {
	local pem_file="$1"
	jq -n --rawfile pem "$pem_file" \
		'{Admin:{Web:0,Unix:"/tmp/almagest-e2e.sock"},TLS:{BuiltinCA:{PEM:$pem}}}' \
		>"$E2E_CONFIG_DIR/protected_alm.hujson"
}

assert_protected_ca() {
	local issuer="$1"
	cli tls builtin-ca | jq -e --arg issuer "$issuer" '
		.cert.source == "builtin"
		and .cert.read_only
		and .immutable
		and .regeneration_disabled
		and .regeneration_disabled_by == "protected_config"
		and .options.issuer == $issuer
	' >/dev/null || fail "protected built-in CA $issuer was not published"
	if cli tls set-builtin-ca --issuer rejected \
		>"$E2E_RUNTIME/protected-mutation.out" \
		2>"$E2E_RUNTIME/protected-mutation.err"; then
		fail "protected built-in CA accepted mutable options"
	fi
}

write_file_config "$E2E_CONFIG_DIR/rsa-combined.pem"
start_daemon
assert_protected_ca "Protected RSA E2E"
stop_daemon

write_inline_config "$E2E_CONFIG_DIR/ec-combined.pem"
start_daemon
assert_protected_ca "Protected ECDSA E2E"
stop_daemon

write_file_config "$E2E_CONFIG_DIR/ed25519-combined.pem"
start_daemon
assert_protected_ca "Protected Ed25519 E2E"
stop_daemon

make_bundle() {
	local name="$1"
	shift
	: >"$E2E_RUNTIME/$name.pem"
	for source in "$@"; do
		cat "$source" >>"$E2E_RUNTIME/$name.pem"
	done
}

make_bundle missing-key "$E2E_CONFIG_DIR/rsa.crt"
make_bundle missing-cert "$E2E_CONFIG_DIR/rsa.key"
make_bundle duplicate-cert "$E2E_CONFIG_DIR/rsa.crt" "$E2E_CONFIG_DIR/ec.crt" "$E2E_CONFIG_DIR/rsa.key"
make_bundle duplicate-key "$E2E_CONFIG_DIR/rsa.crt" "$E2E_CONFIG_DIR/rsa.key" "$E2E_CONFIG_DIR/ec.key"
make_bundle unsupported-block "$E2E_CONFIG_DIR/rsa.crt" "$E2E_CONFIG_DIR/public.pem"
make_bundle mismatched "$E2E_CONFIG_DIR/rsa.crt" "$E2E_CONFIG_DIR/ec.key"
cp "$E2E_CONFIG_DIR/rsa-combined.pem" "$E2E_RUNTIME/trailing.pem"
printf '%s\n' trailing-data >>"$E2E_RUNTIME/trailing.pem"

for name in missing-key missing-cert duplicate-cert duplicate-key unsupported-block mismatched trailing; do
	write_file_config "$E2E_RUNTIME/$name.pem"
	log_file="$E2E_RUNTIME/$name.log"
	XDG_RUNTIME_DIR="$E2E_RUNTIME" almagestd \
		-c "$E2E_CONFIG_DIR/alm.hujson" \
		-p "$E2E_CONFIG_DIR/protected_alm.hujson" \
		--unsafe-config --tmp-file-storage --web 0 -l debug \
		>"$log_file" 2>&1 &
	pid="$!"
	if wait "$pid"; then
		fail "$name protected CA bundle unexpectedly started the daemon"
	fi
	grep -F "protected builtin TLS CA" "$log_file" >/dev/null \
		|| fail "$name protected CA bundle did not report a protected CA error"
done

write_file_config "$E2E_CONFIG_DIR/rsa-combined.pem"
start_daemon
cli admin ping --nonce protected-ca-final | jq -e '.nonce == "protected-ca-final"' >/dev/null \
	|| fail "daemon did not start after malformed protected CA checks"

log "case passed"
