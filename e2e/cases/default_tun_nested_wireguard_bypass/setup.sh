#!/usr/bin/env bash
set -euo pipefail

command -v curl >/dev/null
command -v dig >/dev/null
command -v dnsmasq >/dev/null
command -v ip >/dev/null
command -v jq >/dev/null
command -v nc >/dev/null
command -v wg >/dev/null
test -c /dev/net/tun

router_ns="alm-bypass-router"
peer_ns="alm-bypass-peer"
root_if="almbp0"

ip netns del "$router_ns" 2>/dev/null || true
ip netns del "$peer_ns" 2>/dev/null || true
ip link del "$root_if" 2>/dev/null || true

if ! ip netns add "$router_ns" 2>"$E2E_RUNTIME/netns.err"; then
	echo "default_tun_nested_wireguard_bypass requires privileged Docker networking" >&2
	echo 'run with E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun"' >&2
	cat "$E2E_RUNTIME/netns.err" >&2
	exit 1
fi
ip netns add "$peer_ns"

# Root namespace to router namespace.
ip link add "$root_if" type veth peer name almbp1
ip link set almbp1 netns "$router_ns"
ip addr add 10.230.0.2/24 dev "$root_if"
ip link set "$root_if" up
ip -n "$router_ns" addr add 10.230.0.1/24 dev almbp1
ip -n "$router_ns" link set lo up
ip -n "$router_ns" link set almbp1 up

# Router namespace to the public WireGuard peer namespace.
ip -n "$router_ns" link add almbp2 type veth peer name almbp3
ip -n "$router_ns" link set almbp3 netns "$peer_ns"
ip -n "$router_ns" addr add 203.0.113.1/24 dev almbp2
ip -n "$router_ns" link set almbp2 up
ip -n "$peer_ns" addr add 203.0.113.2/24 dev almbp3
ip -n "$peer_ns" link set lo up
ip -n "$peer_ns" link set almbp3 up
ip -n "$peer_ns" route add default via 203.0.113.1 dev almbp3

ip netns exec "$router_ns" sysctl -qw net.ipv4.ip_forward=1

# Make the endpoint reachable only through the ordinary main-table default.
# All changes stay in this disposable container network namespace.
ip route del default
ip route add default via 10.230.0.1 dev "$root_if"

# Configure a Linux kernel WireGuard peer. The userspace Almagest peer starts
# the handshake, so the kernel peer learns its endpoint dynamically.
printf '%s\n' "SEtMTU5PUFFSU1RVVldYWVpbXF1eX2BhYmNkZWZnaGk=" \
	>"$E2E_RUNTIME/kernel-private.key"
chmod 600 "$E2E_RUNTIME/kernel-private.key"
ip -n "$peer_ns" link add wgbp0 type wireguard
ip -n "$peer_ns" addr add 10.250.0.2/32 dev wgbp0
ip netns exec "$peer_ns" wg set wgbp0 \
	private-key "$E2E_RUNTIME/kernel-private.key" \
	listen-port 51881 \
	peer "f4iMlTOU1YOfovXWCeMMjeBsc6vVqqDE/7XQeHg2oVQ=" \
	allowed-ips 10.250.0.1/32
ip -n "$peer_ns" link set wgbp0 mtu 1420 up
ip -n "$peer_ns" route add 10.250.0.1/32 dev wgbp0

cat >"$E2E_RUNTIME/cleanup-netns.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
ip netns del alm-bypass-router 2>/dev/null || true
ip netns del alm-bypass-peer 2>/dev/null || true
ip link del almbp0 2>/dev/null || true
EOF
chmod +x "$E2E_RUNTIME/cleanup-netns.sh"
