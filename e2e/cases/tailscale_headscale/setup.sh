#!/usr/bin/env bash
set -euo pipefail

command -v headscale >/dev/null
command -v jq >/dev/null
command -v nc >/dev/null

mkdir -p "$E2E_RUNTIME/headscale"
printf '%s\n' '127.0.0.1 headscale.e2e.internal' >>/etc/hosts
sed -i "s@%E2E_RUNTIME%@$E2E_RUNTIME@g" "$E2E_CONFIG_DIR/headscale.yaml"
sed -i "s@%E2E_CONFIG_DIR%@$E2E_CONFIG_DIR@g" "$E2E_CONFIG_DIR/headscale.yaml"
for protected in "$E2E_CONFIG_DIR"/daemon-*/protected_alm.hujson; do
	sed -i "s@%E2E_RUNTIME%@$E2E_RUNTIME@g" "$protected"
done

openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
	-keyout "$E2E_RUNTIME/headscale/ca.key" \
	-out "$E2E_RUNTIME/headscale/ca.crt" \
	-subj "/CN=Almagest Tailscale E2E CA" >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes \
	-keyout "$E2E_RUNTIME/headscale/tls.key" \
	-out "$E2E_RUNTIME/headscale/tls.csr" \
	-subj "/CN=headscale.e2e.internal" >/dev/null 2>&1
printf '%s\n' 'subjectAltName=DNS:headscale.e2e.internal' 'extendedKeyUsage=serverAuth' \
	>"$E2E_RUNTIME/headscale/tls.ext"
openssl x509 -req \
	-in "$E2E_RUNTIME/headscale/tls.csr" \
	-CA "$E2E_RUNTIME/headscale/ca.crt" \
	-CAkey "$E2E_RUNTIME/headscale/ca.key" \
	-CAcreateserial \
	-out "$E2E_RUNTIME/headscale/tls.crt" \
	-days 1 \
	-sha256 \
	-extfile "$E2E_RUNTIME/headscale/tls.ext" >/dev/null 2>&1
