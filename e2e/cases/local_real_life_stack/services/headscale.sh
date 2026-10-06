#!/usr/bin/env bash
set -euo pipefail

shared=/stack-shared
runtime=/tmp/almagest-headscale-service
headscale_dir="$runtime/headscale"
peer_dir="$runtime/peer"
mkdir -p "$headscale_dir" "$peer_dir"
standalone=0
if [[ "${E2E_HEADSCALE_STANDALONE:-0}" == "1" ]]; then
	standalone=1
	: "${E2E_PUBLIC_IP:?}"
	: "${E2E_EXAMPLE_IP:?}"
	self_ip="$(ip -4 route get "$E2E_PUBLIC_IP" | awk '{for (i=1; i<=NF; i++) if ($i == "src") {print $(i+1); exit}}')"
else
	: "${E2E_AMNESIA_IP:?}"
	self_ip="$E2E_AMNESIA_IP"
	# The original case shares the Amnesia service network namespace. The
	# route is created by that service before this container starts.
	ip route get 10.205.10.1 | grep -F 'dev awg-stack' >/dev/null
fi
[[ -n "$self_ip" ]]
printf '%s headscale.stack.test\n' "$self_ip" >>/etc/hosts
mkdir -p "$shared/dns-hosts"
printf '%s headscale.stack.test\n' "$self_ip" >"$shared/dns-hosts/headscale.hosts"

headscale_pid=""
peer_pid=""
capture_pid=""
dns_pid=""
# shellcheck disable=SC2329 # Invoked through the EXIT trap.
cleanup() {
	local status="$?"
	trap - EXIT INT TERM
	if ((status != 0)); then
		for output in "$runtime"/*.log; do
			[[ -f "$output" ]] || continue
			echo "==> $(basename "$output")" >&2
			tail -160 "$output" >&2 || true
		done
	fi
	for pid in "$capture_pid" "$dns_pid" "$peer_pid" "$headscale_pid"; do
		if [[ -n "$pid" ]]; then
			kill "$pid" 2>/dev/null || true
		fi
	done
	for pid in "$capture_pid" "$dns_pid" "$peer_pid" "$headscale_pid"; do
		if [[ -n "$pid" ]]; then
			wait "$pid" 2>/dev/null || true
		fi
	done
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
headscale_ready() {
	curl -fsS --cacert "$headscale_dir/ca.crt" \
		"https://$self_ip:18090/health" >/dev/null 2>&1
}

openssl req -x509 -newkey rsa:2048 -nodes -days 30 \
	-keyout "$headscale_dir/ca.key" \
	-out "$headscale_dir/ca.crt" \
	-subj "/CN=Almagest local Headscale root" \
	-addext "basicConstraints=critical,CA:true,pathlen:0" \
	-addext "keyUsage=critical,keyCertSign,cRLSign" >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes \
	-keyout "$headscale_dir/tls.key" \
	-out "$headscale_dir/tls.csr" \
	-subj "/CN=headscale.stack.test" >/dev/null 2>&1
cat >"$headscale_dir/tls.ext" <<EOF
basicConstraints = critical,CA:false
keyUsage = critical,digitalSignature,keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = DNS:headscale.stack.test,IP:$self_ip,IP:127.0.0.1
EOF
openssl x509 -req \
	-in "$headscale_dir/tls.csr" \
	-CA "$headscale_dir/ca.crt" \
	-CAkey "$headscale_dir/ca.key" \
	-CAcreateserial \
	-out "$headscale_dir/tls.crt" \
	-days 30 -sha256 -extfile "$headscale_dir/tls.ext" >/dev/null 2>&1
install -m 0644 "$headscale_dir/ca.crt" "$shared/headscale-root.pem"

cat >"$headscale_dir/policy.hujson" <<'EOF'
{
	"acls": [{"action": "accept", "src": ["*"], "dst": ["*:*"]}],
}
EOF

cat >"$headscale_dir/headscale.yaml" <<EOF
server_url: https://headscale.stack.test:18090
listen_addr: 0.0.0.0:18090
metrics_listen_addr: ""
grpc_listen_addr: 127.0.0.1:50443
grpc_allow_insecure: true
trusted_proxies: []

noise:
  private_key_path: $headscale_dir/noise_private.key

prefixes:
  v4: 100.64.0.0/10
  v6: fd7a:115c:a1e0::/48
  allocation: sequential

derp:
  server:
    enabled: true
    region_id: 999
    region_code: local
    region_name: Local stack DERP
    verify_clients: true
    stun_listen_addr: 127.0.0.1:13478
    private_key_path: $headscale_dir/derp_server_private.key
    automatically_add_embedded_derp_region: true
  urls: []
  paths: []
  auto_update_enabled: false
  update_frequency: 3h

disable_check_updates: true

node:
  expiry: 0
  ephemeral:
    inactivity_timeout: 30m
  routes:
    ha:
      probe_interval: 0s
      probe_timeout: 1s

database:
  type: sqlite
  debug: false
  sqlite:
    path: $headscale_dir/db.sqlite
    write_ahead_log: true

tls_cert_path: $headscale_dir/tls.crt
tls_key_path: $headscale_dir/tls.key

log:
  level: info
  format: text

policy:
  mode: file
  path: $headscale_dir/policy.hujson

dns:
  magic_dns: true
  base_domain: stack.test
  override_local_dns: false
  nameservers:
    global: []
    split: {}
  search_domains: []
  extra_records: []

unix_socket: $headscale_dir/headscale.sock
unix_socket_permission: "0777"
logtail:
  enabled: false
taildrop:
  enabled: false
EOF

headscale serve --config "$headscale_dir/headscale.yaml" \
	>"$runtime/headscale.log" 2>&1 &
headscale_pid="$!"
retry 30 headscale_ready
headscale --config "$headscale_dir/headscale.yaml" users create stack >/dev/null
auth_key="$(headscale --config "$headscale_dir/headscale.yaml" \
	preauthkeys create --user 1 --reusable --expiration 2h)"
[[ -n "$auth_key" ]]
printf '%s\n' "$auth_key" >"$shared/headscale-auth-key"
chmod 0600 "$shared/headscale-auth-key"
peer_private="$(wg genkey)"

cat >"$peer_dir/alm.hujson" <<'EOF'
{
	"Logs": {
		"Cache": {"Debug": 4096, "Info": 512, "Warn": 512, "Error": 512, "Fatal": 64},
	},
	"nodes": {
		"Tailscale Peer": {
			"Class": "almagest/TailscaleCtrl",
			"Links": [
				"WireGuard -> [WireGuard Peer] WGCtrl",
				"Network -> [Control Network Peer] Network",
				"Firewall -> [Tailnet Firewall Peer] Config",
			],
			"enabled": true,
			"controlURL": "https://headscale.stack.test:18090",
			"tlsConfig": "headscale-tls",
			"hostname": "peer",
			"authKeySecret": "headscale-auth-key",
			"listenPort": 41642,
			"disableDERP": true,
			"disableDiscovery": false,
		},
		"WireGuard Peer": {
			"Class": "almagest/WireGuard",
			"Links": ["Network -> [UDP Network Peer] Network"],
			"privateKeySecret": "wireguard-peer-private",
			"listenPort": 51821,
			"ownedIPs": "",
			"peers": [],
		},
		"VTun Peer": {
			"Class": "almagest/VTun",
			"Links": ["Tun -> [WireGuard Peer] Tun"],
			"name": "tailscale-peer",
			"localAddrs": "",
			"dnsServers": "",
			"mtu": 1280,
			"endpointCh": 1024,
			"eventCh": 10,
			"noLoopbackAddr": false,
			"disableSACK": false,
		},
		"Tailnet Firewall Peer": {
			"Class": "almagest/NetworkFirewall",
			"Links": ["Network -> [VTun Peer] Network"],
			"rules": {},
		},
		"HttpServer SO": {
			"Class": "almagest/HttpServer",
			"Links": ["Network -> [Tailnet Firewall Peer] Network"],
			"host": "0.0.0.0",
			"port": 18080,
			"response": "tailnet HttpServer SO response",
		},
		"Control Network Peer": {"Class": "almagest/OutNetwork"},
		"UDP Network Peer": {"Class": "almagest/OutNetwork"},
	},
}
EOF

cat >"$peer_dir/protected_alm.hujson" <<EOF
{
	"Admin": {"Web": 0, "Unix": ""},
	"TLS": {
		"Certificates": {
			"headscale-root": {"File": "$headscale_dir/ca.crt"},
		},
		"Policies": {
			"headscale-tls": {
				"trust_anchors": [{"cert": "headscale-root", "scope": {"any": true}}],
			},
		},
	},
	"Secrets": {
		"headscale-auth-key": {
			"Value": "$auth_key",
			"Nodes": ["Tailscale Peer"],
		},
		"wireguard-peer-private": {
			"Value": "$peer_private",
			"Nodes": ["WireGuard Peer"],
		},
	},
}
EOF

peer_socket="$runtime/peer.sock"
XDG_RUNTIME_DIR="$runtime" almagestd \
	--no-isolation -c "$peer_dir" -l debug --debug-nodes --experimental --unsafe-config \
	--tmp-file-storage --unix "$peer_socket" --web 0 \
	>"$runtime/peer.log" 2>&1 &
peer_pid="$!"
retry 30 test -S "$peer_socket"

tcpdump -l -nn -tttt -i any \
	'udp and (port 41641 or port 41642 or port 51820 or port 51821)' \
	>"$runtime/tailscale-packets.log" 2>&1 &
capture_pid="$!"

peer_cli() {
	timeout --kill-after=2 5 almagest-cli --url "unix://$peer_socket" "$@"
}
# shellcheck disable=SC2329 # Invoked by retry through its command arguments.
node_label_is() {
	local name="$1"
	local label="$2"
	[[ "$(peer_cli graph node --name "$name" | jq -er '.snapshot.label')" == "$label" ]]
}

retry 60 node_label_is "Tailscale Peer" up || {
	peer_cli graph menu --name "Tailscale Peer" >&2 || true
	exit 1
}
retry 60 node_label_is "WireGuard Peer" up || {
	peer_cli graph menu --name "WireGuard Peer" >&2 || true
	exit 1
}
retry 30 node_label_is "Tailnet Firewall Peer" "filtering 4 rules" || {
	peer_cli graph menu --name "Tailnet Firewall Peer" >&2 || true
	exit 1
}
retry 30 grep -F "listening on 0.0.0.0:18080" "$runtime/peer.log" >/dev/null

# shellcheck disable=SC2329 # Invoked by retry through its command arguments.
standalone_dns_ready() {
	dig +time=1 +tries=1 @"$self_ip" public.stack.test A +short \
		| grep -Fx "$E2E_PUBLIC_IP" >/dev/null
}

if ((standalone)); then
	dnsmasq --no-daemon --keep-in-foreground --log-facility=- \
		--port=53 --listen-address="$self_ip" --bind-interfaces \
		--no-resolv --no-hosts --local-ttl=300 --hostsdir="$shared/dns-hosts" \
		--address="/public.stack.test/$E2E_PUBLIC_IP" \
		--address="/intercept-dns.stack.test/$E2E_EXAMPLE_IP" \
		>"$runtime/dnsmasq.log" 2>&1 &
	dns_pid="$!"
	retry 30 standalone_dns_ready
else
	# A main-container connection to Headscale must use the Amnesia exit. The
	# peer daemon is local to this container and remains allowed through
	# loopback.
	nft -f - <<EOF
table inet headscale_guard {
	chain input {
		type filter hook input priority filter; policy accept;
		iifname "lo" tcp dport 18090 accept
		ip saddr 10.205.10.1 tcp dport 18090 accept
		tcp dport 18090 drop
	}
}
EOF
fi

touch "$shared/headscale-ready"
service_pids=("$headscale_pid" "$peer_pid")
if [[ -n "$dns_pid" ]]; then
	service_pids+=("$dns_pid")
fi
wait -n "${service_pids[@]}"
echo "Headscale, its Tailscale peer, or DNS exited" >&2
exit 1
