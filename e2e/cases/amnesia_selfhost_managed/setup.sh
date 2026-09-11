#!/usr/bin/env bash
set -euo pipefail

for command in amnesia-e2e-tool amneziawg-go curl ip jq openssl ping python3 socat wg; do
	command -v "$command" >/dev/null
done
if [[ ! -c /dev/net/tun ]]; then
	echo "/dev/net/tun is required; run this case with Docker --privileged" >&2
	exit 1
fi

generate_ca() {
	local name="$1"
	local cert="$2"
	local key="$3"
	openssl req -x509 -newkey rsa:2048 -sha256 -nodes -days 30 \
		-subj "/CN=$name" -keyout "$key" -out "$cert" \
		-addext "basicConstraints=critical,CA:true,pathlen:0" \
		-addext "keyUsage=critical,keyCertSign,cRLSign" >/dev/null 2>&1
}

generate_server_cert() {
	local cert="$1"
	local key="$2"
	local csr="$cert.csr"
	local ext="$cert.ext"
	openssl req -newkey rsa:2048 -nodes -subj "/CN=gateway.amnesia.test" \
		-keyout "$key" -out "$csr" >/dev/null 2>&1
	cat >"$ext" <<'EOF'
basicConstraints = critical,CA:false
keyUsage = critical,digitalSignature,keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = DNS:gateway.amnesia.test
EOF
	openssl x509 -req -in "$csr" \
		-CA "$E2E_CONFIG_DIR/amnesia-root.pem" \
		-CAkey "$E2E_CONFIG_DIR/amnesia-root.key" -CAcreateserial \
		-sha256 -days 30 -extfile "$ext" -out "$cert" >/dev/null 2>&1
}

generate_ca "Amnesia E2E Root" "$E2E_CONFIG_DIR/amnesia-root.pem" "$E2E_CONFIG_DIR/amnesia-root.key"
generate_ca "Amnesia Wrong Root" "$E2E_CONFIG_DIR/wrong-root.pem" "$E2E_CONFIG_DIR/wrong-root.key"
generate_server_cert "$E2E_CONFIG_DIR/gateway.pem" "$E2E_CONFIG_DIR/gateway.key"
if ! grep -qE '(^|[[:space:]])gateway\.amnesia\.test([[:space:]]|$)' /etc/hosts; then
	printf '127.0.0.1 gateway.amnesia.test\n' >>/etc/hosts
fi

# make-gateway-profile validates a fixture client key. The service response
# removes that fixture identity so wgo can validate and apply the fresh key
# that each managed Amnesia connection sends in its gateway request.
fixture_private="$(wg genkey)"
fixture_public="$(printf '%s\n' "$fixture_private" | wg pubkey)"
server_private="$(wg genkey)"
server_public="$(printf '%s\n' "$server_private" | wg pubkey)"
header_key="000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
client_listen_port=51862

amnesia-e2e-tool make-gateway-rsa \
	-private-file "$E2E_CONFIG_DIR/gateway-private.pem" \
	-public-file "$E2E_CONFIG_DIR/gateway-public.pem" >/dev/null
amnesia-e2e-tool make-gateway-profile \
	-endpoint "127.0.0.1:51861" \
	-client-public-key "$fixture_public" \
	-server-public-key "$server_public" \
	-client-address "10.205.10.1" \
	-allowed-ip "10.205.20.1/32" \
	-header-protection-key "$header_key" \
	-mtu 1420 -keepalive 1 >"$E2E_CONFIG_DIR/gateway-profile-fixture.vpn"

python3 - "$E2E_CONFIG_DIR/gateway-profile-fixture.vpn" \
	"$E2E_CONFIG_DIR/gateway-profile.vpn" "$client_listen_port" <<'PY'
import base64
import json
import sys
import zlib

source, target, listen_port = sys.argv[1:]
text = open(source, encoding="utf-8").read().strip().removeprefix("vpn://")
text += "=" * (-len(text) % 4)
framed = base64.urlsafe_b64decode(text)
document = json.loads(zlib.decompress(framed[4:]))
protocol = document["containers"][0]["awg"]
protocol.pop("client_pub_key", None)
# A random handshake trailer can grow to 65535 bytes. It cannot fit in the
# maximum IPv4 UDP payload of 65507 bytes, so a usable managed profile must
# disable it. The WGO compatibility case tests random trailers separately.
protocol["RandomTrailers"] = "false"
client = json.loads(protocol["last_config"])
client.pop("client_pub_key", None)
client.pop("client_priv_key", None)
client["RandomTrailers"] = "false"
# The server peer is installed after the managed node reports its generated
# key. A fixed client port lets the server initiate the first usable handshake
# without depending on retransmission of the client's earlier unknown-key
# initiation.
client["config"] = (
    "[Interface]\n"
    "PrivateKey = $WIREGUARD_CLIENT_PRIVATE_KEY\n"
    "ListenPort = " + listen_port + "\n"
)
protocol["last_config"] = json.dumps(client, separators=(",", ":"))
plain = json.dumps(document, separators=(",", ":")).encode()
payload = b"\x00\x00\x00\xff" + zlib.compress(plain, 6)
encoded = base64.urlsafe_b64encode(payload).decode().rstrip("=")
open(target, "w", encoding="utf-8").write("vpn://" + encoded + "\n")
PY

amnesia-e2e-tool make-activation-key -country ge >"$E2E_CONFIG_DIR/activation-one.vpn"
python3 - "$E2E_CONFIG_DIR/activation-one.vpn" "$E2E_CONFIG_DIR/activation-two.vpn" <<'PY'
import base64
import json
import sys
import zlib

source, target = sys.argv[1:]
text = open(source, encoding="utf-8").read().strip().removeprefix("vpn://")
text += "=" * (-len(text) % 4)
framed = base64.urlsafe_b64decode(text)
document = json.loads(zlib.decompress(framed[4:]))
document["auth_data"]["api_key"] = "c4f9a207e15d683b"
plain = json.dumps(document, separators=(",", ":")).encode()
updated = b"\x00\x00\x00\xff" + zlib.compress(plain, 6)
value = base64.urlsafe_b64encode(updated).decode().rstrip("=")
open(target, "w", encoding="utf-8").write("vpn://" + value + "\n")
PY

server_private_hex="$(printf '%s' "$server_private" | base64 -d | od -An -tx1 -v | tr -d ' \n')"
{
	printf 'SERVER_PRIVATE_HEX=%q\n' "$server_private_hex"
	printf 'HEADER_KEY=%q\n' "$header_key"
	printf 'CLIENT_LISTEN_PORT=%q\n' "$client_listen_port"
} >"$E2E_CONFIG_DIR/amnesia.env"

cat >"$E2E_CONFIG_DIR/protected_alm.hujson" <<EOF
{
	"Admin": {"Web": 0, "Unix": "$E2E_ADMIN_SOCK"},
}
EOF

if [[ -f /usr/local/share/amneziawg-go.commit ]]; then
	printf 'amneziawg-go commit: %s\n' "$(cat /usr/local/share/amneziawg-go.commit)" >&2
fi
