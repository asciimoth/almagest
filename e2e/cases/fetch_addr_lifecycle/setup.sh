#!/usr/bin/env bash
set -euo pipefail

command -v jq >/dev/null
command -v openssl >/dev/null
command -v python3 >/dev/null

openssl req \
	-x509 \
	-newkey rsa:2048 \
	-sha256 \
	-nodes \
	-days 30 \
	-subj "/CN=Almagest FetchAddr E2E Root" \
	-keyout "$E2E_CONFIG_DIR/fetch-ca.key" \
	-out "$E2E_CONFIG_DIR/fetch-ca.pem" \
	-addext "basicConstraints=critical,CA:true,pathlen:0" \
	-addext "keyUsage=critical,keyCertSign,cRLSign" \
	>/dev/null 2>&1

openssl req \
	-newkey rsa:2048 \
	-nodes \
	-subj "/CN=ifconfig.co" \
	-keyout "$E2E_CONFIG_DIR/fetch-server.key" \
	-out "$E2E_CONFIG_DIR/fetch-server.csr" \
	>/dev/null 2>&1

cat >"$E2E_CONFIG_DIR/fetch-server.ext" <<'EOF'
basicConstraints = critical,CA:false
keyUsage = critical,digitalSignature,keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = @alt_names
[alt_names]
DNS.1 = ifconfig.co
DNS.2 = api.ipify.org
EOF

openssl x509 \
	-req \
	-in "$E2E_CONFIG_DIR/fetch-server.csr" \
	-CA "$E2E_CONFIG_DIR/fetch-ca.pem" \
	-CAkey "$E2E_CONFIG_DIR/fetch-ca.key" \
	-CAcreateserial \
	-sha256 \
	-days 30 \
	-extfile "$E2E_CONFIG_DIR/fetch-server.ext" \
	-out "$E2E_CONFIG_DIR/fetch-server.pem" \
	>/dev/null 2>&1

trusted_dir="$E2E_ISOLATION_ROOT/protected-inputs"
mkdir -p "$trusted_dir"
install -o 0 -g 0 -m 0600 "$E2E_CONFIG_DIR/fetch-ca.pem" \
	"$trusted_dir/fetch-ca.pem"
chmod 0700 "$trusted_dir"

if ! grep -qE '(^|[[:space:]])ifconfig\.co([[:space:]]|$)' /etc/hosts; then
	printf '127.0.0.1 ifconfig.co api.ipify.org\n' >>/etc/hosts
fi

cat >"$E2E_CONFIG_DIR/protected_alm.hujson" <<EOF
{
	"Admin": {
		"Web": 0,
		"Unix": "$E2E_ADMIN_SOCK",
	},
	"TLS": {
		"Certificates": {
			"fetch-address-ca": {
				"File": "$trusted_dir/fetch-ca.pem",
			},
		},
		"Policies": {
			"fetch-address-test": {
				"trust_anchors": [
					{"cert": "fetch-address-ca", "scope": {"any": true}},
				],
			},
		},
	},
}
EOF
