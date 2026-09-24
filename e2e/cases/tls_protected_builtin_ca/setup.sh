#!/usr/bin/env bash
set -euo pipefail

for command in almagest-cli jq openssl; do
	command -v "$command" >/dev/null
done

make_ca_cert() {
	local key="$1"
	local cert="$2"
	local name="$3"
	openssl req -new -x509 -sha256 -days 2 -key "$key" \
		-subj "/CN=$name" \
		-addext "basicConstraints=critical,CA:true,pathlen:0" \
		-addext "keyUsage=critical,keyCertSign,cRLSign" \
		-out "$cert" >/dev/null 2>&1
}

openssl genrsa -traditional -out "$E2E_CONFIG_DIR/rsa.key" 2048 >/dev/null 2>&1
make_ca_cert "$E2E_CONFIG_DIR/rsa.key" "$E2E_CONFIG_DIR/rsa.crt" "Protected RSA E2E"

openssl ecparam -name secp384r1 -genkey -noout \
	-out "$E2E_CONFIG_DIR/ec.key" >/dev/null 2>&1
make_ca_cert "$E2E_CONFIG_DIR/ec.key" "$E2E_CONFIG_DIR/ec.crt" "Protected ECDSA E2E"

openssl genpkey -algorithm ED25519 -out "$E2E_CONFIG_DIR/ed25519.key" >/dev/null 2>&1
make_ca_cert "$E2E_CONFIG_DIR/ed25519.key" "$E2E_CONFIG_DIR/ed25519.crt" "Protected Ed25519 E2E"

for kind in rsa ec ed25519; do
	{
		cat "$E2E_CONFIG_DIR/$kind.crt"
		cat "$E2E_CONFIG_DIR/$kind.key"
	} >"$E2E_CONFIG_DIR/$kind-combined.pem"
done
openssl pkey -in "$E2E_CONFIG_DIR/rsa.key" -pubout \
	-out "$E2E_CONFIG_DIR/public.pem" >/dev/null 2>&1
