#!/usr/bin/env bash
set -euo pipefail

command -v jq >/dev/null
command -v openssl >/dev/null

generate_ca() {
	local cn="$1"
	local cert="$2"
	local key="$3"

	openssl req \
		-x509 \
		-newkey rsa:2048 \
		-sha256 \
		-nodes \
		-days 3650 \
		-subj "/CN=$cn" \
		-keyout "$key" \
		-out "$cert" \
		-addext "basicConstraints=critical,CA:true,pathlen:0" \
		-addext "keyUsage=critical,keyCertSign,cRLSign" \
		>/dev/null 2>&1
}

mkdir -p "$E2E_CONFIG_DIR/empty-certs"
generate_ca "E2E Reload Initial Root" "$E2E_CONFIG_DIR/initial-root.pem" "$E2E_CONFIG_DIR/initial-root.key"
generate_ca "E2E Reload Replacement Root" "$E2E_CONFIG_DIR/replacement-root.pem" "$E2E_CONFIG_DIR/replacement-root.key"

cat \
	"$E2E_CONFIG_DIR/initial-root.pem" \
	"$E2E_CONFIG_DIR/fixtures/narrow-scope-ca.pem" \
	"$E2E_CONFIG_DIR/fixtures/warning-interception-root.pem" \
	>"$E2E_CONFIG_DIR/os-bundle.pem"
