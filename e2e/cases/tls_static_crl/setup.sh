#!/usr/bin/env bash
set -euo pipefail

command -v jq >/dev/null
command -v openssl >/dev/null

trusted_dir="$E2E_ISOLATION_ROOT/protected-inputs"
mkdir -p "$trusted_dir" "$E2E_CONFIG_DIR/empty-certs"

openssl req -x509 -newkey rsa:2048 -sha256 -nodes -days 3650 \
	-subj "/CN=E2E Revoked OS Root" \
	-keyout "$trusted_dir/root.key" -out "$trusted_dir/root.pem" \
	-addext "basicConstraints=critical,CA:true,pathlen:1" \
	-addext "keyUsage=critical,keyCertSign,cRLSign" >/dev/null 2>&1

: >"$trusted_dir/index.txt"
printf '1000\n' >"$trusted_dir/serial"
printf '1000\n' >"$trusted_dir/crlnumber"
cat >"$trusted_dir/openssl.cnf" <<EOF
[ ca ]
default_ca = local_ca
[ local_ca ]
database = $trusted_dir/index.txt
serial = $trusted_dir/serial
crlnumber = $trusted_dir/crlnumber
certificate = $trusted_dir/root.pem
private_key = $trusted_dir/root.key
default_md = sha256
default_crl_days = 30
policy = policy_any
[ policy_any ]
commonName = supplied
EOF
openssl ca -batch -config "$trusted_dir/openssl.cnf" -revoke "$trusted_dir/root.pem" >/dev/null 2>&1
openssl ca -batch -config "$trusted_dir/openssl.cnf" -gencrl -out "$trusted_dir/revoked.crl.pem" >/dev/null 2>&1

chmod 0700 "$trusted_dir"
chmod 0600 "$trusted_dir"/*
cat >"$E2E_CONFIG_DIR/protected_alm.hujson" <<EOF
{
	"Admin": {"Web": 0, "Unix": "$E2E_ADMIN_SOCK"},
	"TLS": {
		"CRLs": {
			"Enabled": true,
			"Lists": {"e2e-root-revocations": {"File": "$trusted_dir/revoked.crl.pem"}},
		},
	},
}
EOF
