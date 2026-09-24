#!/usr/bin/env bash
set -euo pipefail

for command in almagest-cli jq openssl; do
	command -v "$command" >/dev/null
done

openssl req -x509 -newkey rsa:2048 -sha256 -nodes -days 2 \
	-subj "/CN=Admin Surface E2E CA" \
	-addext "basicConstraints=critical,CA:true,pathlen:0" \
	-addext "keyUsage=critical,keyCertSign,cRLSign" \
	-keyout "$E2E_CONFIG_DIR/ca.key" -out "$E2E_CONFIG_DIR/ca.pem" >/dev/null 2>&1

