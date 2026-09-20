#!/usr/bin/env bash
set -euo pipefail

: "${E2E_PUBLIC_IP:?}"
: "${E2E_EXAMPLE_IP:?}"
shared=/stack-shared
runtime=/tmp/almagest-amnesia-service
interface=awg-stack
mkdir -p "$runtime" /var/run/amneziawg "$shared/dns-hosts"
internet_if="$(ip -4 route get "$E2E_PUBLIC_IP" | awk '{for (i=1; i<=NF; i++) if ($i == "dev") {print $(i+1); exit}}')"
control_if="$(ip -4 -o addr show scope global | awk -v internet="$internet_if" '$2 != internet {print $2; exit}')"
self_ip="$(ip -4 -o addr show dev "$control_if" scope global | awk '{split($4, a, "/"); print a[1]; exit}')"
[[ -n "$internet_if" && -n "$control_if" && -n "$self_ip" && "$internet_if" != "$control_if" ]]

pids=()
# shellcheck disable=SC2329 # Invoked through the EXIT trap.
cleanup() {
	local status="$?"
	trap - EXIT INT TERM
	if ((status != 0)); then
		for output in "$runtime"/*.log; do
			[[ -f "$output" ]] || continue
			echo "==> $(basename "$output")" >&2
			tail -120 "$output" >&2 || true
		done
	fi
	for pid in "${pids[@]}"; do
		kill "$pid" 2>/dev/null || true
	done
	for pid in "${pids[@]}"; do
		wait "$pid" 2>/dev/null || true
	done
	ip link del "$interface" 2>/dev/null || true
	exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

retry() {
	local timeout="$1"
	shift
	local deadline=$((SECONDS + timeout))
	while ((SECONDS <= deadline)); do
		"$@" && return 0
		sleep 0.25
	done
	return 1
}

# shellcheck disable=SC2329 # Invoked by retry through its command arguments.
dns_ready() {
	dig +time=1 +tries=1 @"$self_ip" -p 53 public.stack.test A \
		+noall +answer \
		| awk -v expected="$E2E_PUBLIC_IP" '
			$2 ~ /^[0-9]+$/ && $2 > 0 && $4 == "A" && $5 == expected {
				found=1
			}
			END { exit !found }
		'
}

generate_ca() {
	local cert="$1"
	local key="$2"
	openssl req -x509 -newkey rsa:2048 -sha256 -nodes -days 30 \
		-subj "/CN=Almagest local Amnesia root" \
		-keyout "$key" -out "$cert" \
		-addext "basicConstraints=critical,CA:true,pathlen:0" \
		-addext "keyUsage=critical,keyCertSign,cRLSign" >/dev/null 2>&1
}

generate_server_cert() {
	local cert="$1"
	local key="$2"
	local csr="$runtime/gateway.csr"
	local ext="$runtime/gateway.ext"
	openssl req -newkey rsa:2048 -nodes -subj "/CN=amnesia.stack.test" \
		-keyout "$key" -out "$csr" >/dev/null 2>&1
	cat >"$ext" <<EOF
basicConstraints = critical,CA:false
keyUsage = critical,digitalSignature,keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = DNS:amnesia.stack.test,IP:$self_ip
EOF
	openssl x509 -req -in "$csr" \
		-CA "$runtime/amnesia-root.pem" \
		-CAkey "$runtime/amnesia-root.key" -CAcreateserial \
		-sha256 -days 30 -extfile "$ext" -out "$cert" >/dev/null 2>&1
}

uapi_set() {
	local payload="$1"
	local reply
	reply="$(printf '%s\n\n' "$payload" | socat - "UNIX-CONNECT:/var/run/amneziawg/$interface.sock")"
	printf '%s\n\n' "$reply" >>"$runtime/amnezia-uapi.log"
	grep -q '^errno=0$' <<<"$reply"
}

generate_ca "$runtime/amnesia-root.pem" "$runtime/amnesia-root.key"
generate_server_cert "$runtime/gateway.pem" "$runtime/gateway.key"

fixture_private="$(wg genkey)"
fixture_public="$(printf '%s\n' "$fixture_private" | wg pubkey)"
server_private="$(wg genkey)"
server_public="$(printf '%s\n' "$server_private" | wg pubkey)"
server_private_hex="$(printf '%s' "$server_private" | base64 -d | od -An -tx1 -v | tr -d ' \n')"
header_key="$(openssl rand -hex 32)"
client_listen_port=51862

amnesia-e2e-tool make-gateway-rsa \
	-private-file "$runtime/gateway-private.pem" \
	-public-file "$runtime/gateway-public.pem" >/dev/null
amnesia-e2e-tool make-gateway-profile \
	-endpoint "$self_ip:51861" \
	-client-public-key "$fixture_public" \
	-server-public-key "$server_public" \
	-client-address "10.205.10.1" \
	-allowed-ip "0.0.0.0/0" \
	-header-protection-key "$header_key" \
	-mtu 1420 -keepalive 1 >"$runtime/gateway-profile-fixture.vpn"

python3 - "$runtime/gateway-profile-fixture.vpn" \
	"$runtime/gateway-profile.vpn" "$client_listen_port" "$self_ip" <<'PY'
import base64
import json
import sys
import zlib

source, target, listen_port, dns_ip = sys.argv[1:]
text = open(source, encoding="utf-8").read().strip().removeprefix("vpn://")
text += "=" * (-len(text) % 4)
framed = base64.urlsafe_b64decode(text)
document = json.loads(zlib.decompress(framed[4:]))
document["dns1"] = dns_ip
protocol = document["containers"][0]["awg"]
protocol.pop("client_pub_key", None)
protocol["RandomTrailers"] = "false"
client = json.loads(protocol["last_config"])
client.pop("client_pub_key", None)
client.pop("client_priv_key", None)
client["RandomTrailers"] = "false"
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

amnesia-e2e-tool make-activation-key -country ge >"$runtime/activation.vpn"
install -m 0644 "$runtime/amnesia-root.pem" "$shared/amnesia-root.pem"
install -m 0644 "$runtime/gateway-public.pem" "$shared/amnesia-gateway-public.pem"
install -m 0644 "$runtime/activation.vpn" "$shared/amnesia-activation.vpn"

WG_PROCESS_FOREGROUND=1 LOG_LEVEL=verbose amneziawg-go -f "$interface" \
	>"$runtime/amneziawg-go.log" 2>&1 &
pids+=("$!")
retry 30 test -S "/var/run/amneziawg/$interface.sock"
ip addr replace 10.205.20.1/32 dev "$interface"
ip link set dev "$interface" mtu 1420 up
ip route replace 10.205.10.1/32 dev "$interface"

# Keep a text trace of the nested Tailscale traffic. The case runner prints it
# only after a failure. This makes intermittent transport failures diagnosable
# without sharing the host network namespace or retaining a packet capture.
tcpdump -l -nn -tttt -i "$interface" \
	'udp and (port 41641 or port 41642 or port 51820 or port 51821)' \
	>"$runtime/tailscale-awg-packets.log" 2>&1 &
pids+=("$!")
tcpdump -l -nn -tttt -i "$control_if" \
	'udp and (port 41641 or port 41642 or port 51820 or port 51821)' \
	>"$runtime/tailscale-control-packets.log" 2>&1 &
pids+=("$!")

uapi_set "$(cat <<EOF
set=1
private_key=$server_private_hex
listen_port=51861
jc=2
jmin=11
jmax=23
s1=12
s2=12
s3=12
s4=12
h1=6111
h2=6222
h3=6333
h4=6444
i1=<b 0xaa55><rc 3><rd 2><t>
i2=<r 5>
i3=<rd 4>
i4=<rc 6>
i5=<b 0x01020304>
header_protection_key=$header_key
content_padding_addition=4-16
rekey_after_time=90-120
rekey_timeout=5-8
reject_after_time=180-220
keepalive_timeout=10-15
max_handshake_attempts=3-5
random_trailers=false
disable_cookies=true
replace_peers=true
EOF
)"

sysctl -qw net.ipv4.ip_forward=1
nft -f - <<EOF
table ip stack_nat {
	chain postrouting {
		type nat hook postrouting priority srcnat; policy accept;
		ip saddr 10.205.10.1 oifname "$internet_if" masquerade
	}
}
table inet stack_forward {
	chain forward {
		type filter hook forward priority -10; policy accept;
		iifname "$interface" oifname "$control_if" ip saddr 10.205.10.1 counter accept
		iifname "$interface" oifname "$internet_if" ip saddr 10.205.10.1 counter accept
		iifname "$control_if" oifname "$interface" ip daddr 10.205.10.1 counter accept
		iifname "$internet_if" oifname "$interface" ip daddr 10.205.10.1 counter accept
	}
}
EOF

dnsmasq --no-daemon --keep-in-foreground --log-facility=- \
	--port=53 --listen-address="$self_ip" --bind-interfaces \
	--no-resolv --no-hosts --local-ttl=300 --hostsdir="$shared/dns-hosts" \
	--address="/public.stack.test/$E2E_PUBLIC_IP" \
	--address="/intercept-dns.stack.test/$E2E_EXAMPLE_IP" \
	>"$runtime/dnsmasq.log" 2>&1 &
pids+=("$!")

amnesia-e2e-tool serve-gateway -listen 127.0.0.1:18091 \
	-private-key "$runtime/gateway-private.pem" \
	-config-file "$runtime/gateway-profile.vpn" \
	>"$runtime/gateway.log" 2>&1 &
pids+=("$!")
retry 30 curl -fsS http://127.0.0.1:18091/health >/dev/null

python3 -u - 18490 18091 "$runtime/gateway.pem" "$runtime/gateway.key" <<'PY' \
	>"$runtime/gateway-tls.log" 2>&1 &
import http.client
import http.server
import ssl
import sys

listen_port, upstream_port = map(int, sys.argv[1:3])
cert, key = sys.argv[3:]

class Handler(http.server.BaseHTTPRequestHandler):
    def forward(self):
        size = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(size)
        headers = {k: v for k, v in self.headers.items()
                   if k.lower() not in {"host", "connection", "content-length"}}
        upstream = http.client.HTTPConnection("127.0.0.1", upstream_port, timeout=15)
        upstream.request(self.command, self.path, body=body, headers=headers)
        response = upstream.getresponse()
        data = response.read()
        self.send_response(response.status)
        for name, value in response.getheaders():
            if name.lower() not in {"connection", "content-length", "transfer-encoding"}:
                self.send_header(name, value)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)
        upstream.close()

    do_GET = forward
    do_POST = forward

    def log_message(self, fmt, *args):
        return

server = http.server.ThreadingHTTPServer(("0.0.0.0", listen_port), Handler)
context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
context.load_cert_chain(cert, key)
server.socket = context.wrap_socket(server.socket, server_side=True)
server.serve_forever()
PY
pids+=("$!")

(
	last_key=""
	while true; do
		if [[ -s "$shared/amnesia-client-peer" ]]; then
			read -r client_ip public_key <"$shared/amnesia-client-peer"
			if [[ -n "$client_ip" && -n "$public_key" && "$public_key" != "$last_key" ]]; then
				public_key_hex="$(printf '%s' "$public_key" | base64 -d | od -An -tx1 -v | tr -d ' \n')"
				uapi_set "$(cat <<EOF
set=1
replace_peers=true
public_key=$public_key_hex
protocol_version=1
endpoint=$client_ip:$client_listen_port
replace_allowed_ips=true
allowed_ip=10.205.10.1/32
persistent_keepalive_interval=1
EOF
)"
				last_key="$public_key"
				touch "$shared/amnesia-peer-ready"
			fi
		fi
		sleep 0.1
	done
) >"$runtime/peer-monitor.log" 2>&1 &
pids+=("$!")

retry 30 curl -fsS "http://$E2E_PUBLIC_IP/health" >/dev/null
retry 30 curl -fsS "http://$E2E_EXAMPLE_IP/health" >/dev/null
retry 30 dns_ready
retry 30 curl -fsS --cacert "$runtime/amnesia-root.pem" \
	"https://$self_ip:18490/health" >/dev/null
touch "$shared/amnesia-ready"

wait -n "${pids[@]}"
echo "an Amnesia service process exited" >&2
exit 1
