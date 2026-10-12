#!/usr/bin/env bash
set -euo pipefail

command -v jq >/dev/null
command -v openssl >/dev/null
command -v stat >/dev/null

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

generate_ca "E2E Protected Root" "$E2E_CONFIG_DIR/protected-ca.pem" "$E2E_CONFIG_DIR/protected-ca.key"
generate_ca "E2E Manual Root" "$E2E_CONFIG_DIR/manual-ca.pem" "$E2E_CONFIG_DIR/manual-ca.key"
generate_ca "E2E Persist Root" "$E2E_CONFIG_DIR/persist-ca.pem" "$E2E_CONFIG_DIR/persist-ca.key"
generate_ca "E2E Admin System Root" "$E2E_CONFIG_DIR/os-root.pem" "$E2E_CONFIG_DIR/os-root.key"
cp "$E2E_CONFIG_DIR/os-root.pem" "$E2E_CONFIG_DIR/os-bundle.pem"

trusted_dir="$E2E_ISOLATION_ROOT/protected-inputs"
mkdir -p "$trusted_dir"
install -o 0 -g 0 -m 0600 "$E2E_CONFIG_DIR/protected-ca.pem" \
	"$trusted_dir/protected-ca.pem"
chmod 0700 "$trusted_dir"

cat >"$E2E_CONFIG_DIR/protected_alm.hujson" <<EOF
{
	"Admin": {
		"Web": 0,
		"Unix": "$E2E_ADMIN_SOCK",
	},
	"TLS": {
		"Certificates": {
			"protected-ca": {
				"File": "$trusted_dir/protected-ca.pem",
			},
		},
		"Policies": {
			"protected-policy": {
				"trust_anchors": [
					{"cert": "protected-ca", "scope": {"any": true}},
				],
			},
		},
	},
}
EOF
