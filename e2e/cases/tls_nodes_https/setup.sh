#!/usr/bin/env bash
set -euo pipefail

command -v curl >/dev/null
command -v jq >/dev/null
command -v openssl >/dev/null
command -v python3 >/dev/null

generate_ca() {
	local cn="$1"
	local cert="$2"
	local key="$3"

	openssl req \
		-x509 \
		-newkey rsa:2048 \
		-sha256 \
		-nodes \
		-days 365 \
		-subj "/CN=$cn" \
		-keyout "$key" \
		-out "$cert" \
		-addext "basicConstraints=critical,CA:true,pathlen:0" \
		-addext "keyUsage=critical,keyCertSign,cRLSign" \
		>/dev/null 2>&1
}

generate_server_cert() {
	local cn="$1"
	local cert="$2"
	local key="$3"
	shift 3
	local csr="$cert.csr"
	local ext="$cert.ext"
	local san_entries=()
	local index=1
	local name

	for name in "$@"; do
		if [[ "$name" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
			san_entries+=("IP.$index = $name")
		else
			san_entries+=("DNS.$index = $name")
		fi
		index=$((index + 1))
	done

	openssl req \
		-newkey rsa:2048 \
		-nodes \
		-subj "/CN=$cn" \
		-keyout "$key" \
		-out "$csr" \
		>/dev/null 2>&1

	{
		echo "basicConstraints = critical,CA:false"
		echo "keyUsage = critical,digitalSignature,keyEncipherment"
		echo "extendedKeyUsage = serverAuth"
		echo "subjectAltName = @alt_names"
		echo "[alt_names]"
		printf '%s\n' "${san_entries[@]}"
	} >"$ext"

	openssl x509 \
		-req \
		-in "$csr" \
		-CA "$E2E_CONFIG_DIR/origin-ca.pem" \
		-CAkey "$E2E_CONFIG_DIR/origin-ca.key" \
		-CAcreateserial \
		-sha256 \
		-days 30 \
		-extfile "$ext" \
		-out "$cert" \
		>/dev/null 2>&1
}

generate_ca "E2E TLS Origin Root" "$E2E_CONFIG_DIR/origin-ca.pem" "$E2E_CONFIG_DIR/origin-ca.key"
generate_server_cert "origin.test" "$E2E_CONFIG_DIR/origin.pem" "$E2E_CONFIG_DIR/origin.key" origin.test
generate_server_cert "wrapper-origin.test" "$E2E_CONFIG_DIR/wrapper-origin.pem" "$E2E_CONFIG_DIR/wrapper-origin.key" wrapper-origin.test

if ! grep -qE '(^|[[:space:]])origin\.test([[:space:]]|$)' /etc/hosts; then
	printf '127.0.0.1 origin.test terminator.test wrapper-origin.test\n' >>/etc/hosts
fi

cat >"$E2E_CONFIG_DIR/protected_alm.hujson" <<EOF
{
	"Admin": {
		"Web": 0,
		"Unix": "$E2E_ADMIN_SOCK",
	},
	"TLS": {
		"Certificates": {
			"origin-ca": {
				"File": "$E2E_CONFIG_DIR/origin-ca.pem",
			},
		},
		"Policies": {
			"origin-policy": {
				"trust_anchors": [
					{"cert": "origin-ca", "scope": {"any": true}},
				],
			},
		},
	},
}
EOF
