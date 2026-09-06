#!/usr/bin/env bash
set -euo pipefail

command -v headscale >/dev/null
command -v jq >/dev/null
command -v nc >/dev/null

printf '%s\n' \
	'127.0.0.1 headscale-a.e2e.internal' \
	'127.0.0.1 headscale-b.e2e.internal' >>/etc/hosts

for server in a b; do
	directory="$E2E_RUNTIME/headscale-$server"
	hostname="headscale-$server.e2e.internal"
	mkdir -p "$directory"
	sed -i "s@%E2E_RUNTIME%@$E2E_RUNTIME@g" "$E2E_CONFIG_DIR/headscale-$server.yaml"
	openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
		-keyout "$directory/ca.key" \
		-out "$directory/ca.crt" \
		-subj "/CN=Almagest multi-Headscale E2E CA $server" >/dev/null 2>&1
	openssl req -newkey rsa:2048 -nodes \
		-keyout "$directory/tls.key" \
		-out "$directory/tls.csr" \
		-subj "/CN=$hostname" >/dev/null 2>&1
	printf '%s\n' "subjectAltName=DNS:$hostname" 'extendedKeyUsage=serverAuth' \
		>"$directory/tls.ext"
	openssl x509 -req \
		-in "$directory/tls.csr" \
		-CA "$directory/ca.crt" \
		-CAkey "$directory/ca.key" \
		-CAcreateserial \
		-out "$directory/tls.crt" \
		-days 1 \
		-sha256 \
		-extfile "$directory/tls.ext" >/dev/null 2>&1
done

sed -i "s@%E2E_RUNTIME%@$E2E_RUNTIME@g" "$E2E_CONFIG_DIR/protected_alm.hujson"
