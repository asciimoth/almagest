#!/usr/bin/env bash
set -euo pipefail

command -v curl >/dev/null
command -v ip >/dev/null
command -v jq >/dev/null
command -v python3 >/dev/null
command -v wg >/dev/null
test -c /dev/net/tun

router_ns="alm-source-router"
peer_ns="alm-source-peer"
root_if="almsr0"

ip netns del "$router_ns" 2>/dev/null || true
ip netns del "$peer_ns" 2>/dev/null || true
ip link del "$root_if" 2>/dev/null || true

if ! ip netns add "$router_ns" 2>"$E2E_RUNTIME/netns.err"; then
	echo "default_tun_source_routes requires privileged Docker networking" >&2
	echo 'run with E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun"' >&2
	cat "$E2E_RUNTIME/netns.err" >&2
	exit 1
fi
ip netns add "$peer_ns"

# Root namespace to router namespace.
ip link add "$root_if" type veth peer name almsr1
ip link set almsr1 netns "$router_ns"
ip addr add 10.231.0.2/24 dev "$root_if"
ip link set "$root_if" up
ip -n "$router_ns" addr add 10.231.0.1/24 dev almsr1
ip -n "$router_ns" link set lo up
ip -n "$router_ns" link set almsr1 up

# Router namespace to the namespace that owns both kernel WireGuard peers.
ip -n "$router_ns" link add almsr2 type veth peer name almsr3
ip -n "$router_ns" link set almsr3 netns "$peer_ns"
ip -n "$router_ns" addr add 203.0.113.1/24 dev almsr2
ip -n "$router_ns" link set almsr2 up
ip -n "$peer_ns" addr add 203.0.113.2/24 dev almsr3
ip -n "$peer_ns" link set lo up
ip -n "$peer_ns" link set almsr3 up
ip -n "$peer_ns" route add default via 203.0.113.1 dev almsr3

ip netns exec "$router_ns" sysctl -qw net.ipv4.ip_forward=1
ip netns exec "$peer_ns" sysctl -qw net.ipv6.conf.all.disable_ipv6=0

# Force both userspace WireGuard transports to use the ordinary main-table
# default. All changes stay inside the disposable E2E container.
ip route del default
ip route add default via 10.231.0.1 dev "$root_if"

# Default-route kernel WireGuard peer.
printf '%s\n' "SEtMTU5PUFFSU1RVVldYWVpbXF1eX2BhYmNkZWZnaGk=" \
	>"$E2E_RUNTIME/default-kernel-private.key"
chmod 600 "$E2E_RUNTIME/default-kernel-private.key"
ip -n "$peer_ns" link add wgsr0 type wireguard
ip -n "$peer_ns" addr add 10.250.0.2/32 dev wgsr0
ip -n "$peer_ns" -6 addr add fd10:250::2/128 dev wgsr0
ip netns exec "$peer_ns" wg set wgsr0 \
	private-key "$E2E_RUNTIME/default-kernel-private.key" \
	listen-port 51881 \
	peer "f4iMlTOU1YOfovXWCeMMjeBsc6vVqqDE/7XQeHg2oVQ=" \
	allowed-ips "10.250.0.1/32,fd10:250::1/128"
ip -n "$peer_ns" link set wgsr0 mtu 1420 up
ip -n "$peer_ns" route add 10.250.0.1/32 dev wgsr0
ip -n "$peer_ns" -6 route add fd10:250::1/128 dev wgsr0

# More-specific overlay kernel WireGuard peer.
printf '%s\n' "UlNUVVZXWFlaW1xdXl9gYWJjZGVmZ2hpamtsbW5vcHE=" \
	>"$E2E_RUNTIME/overlay-kernel-private.key"
chmod 600 "$E2E_RUNTIME/overlay-kernel-private.key"
ip -n "$peer_ns" link add wgsr1 type wireguard
ip -n "$peer_ns" addr add 100.100.0.2/32 dev wgsr1
ip -n "$peer_ns" -6 addr add fd7a:115c:a1e0::2/128 dev wgsr1
ip netns exec "$peer_ns" wg set wgsr1 \
	private-key "$E2E_RUNTIME/overlay-kernel-private.key" \
	listen-port 51882 \
	peer "iPdQZNB08/wCyBwSw7rTAET5KidcvhELeBdkV5XTjF4=" \
	allowed-ips "100.100.0.1/32,fd7a:115c:a1e0::1/128"
ip -n "$peer_ns" link set wgsr1 mtu 1420 up
ip -n "$peer_ns" route add 100.100.0.1/32 dev wgsr1
ip -n "$peer_ns" -6 route add fd7a:115c:a1e0::1/128 dev wgsr1

cat >"$E2E_RUNTIME/cleanup-netns.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
ip netns del alm-source-router 2>/dev/null || true
ip netns del alm-source-peer 2>/dev/null || true
ip link del almsr0 2>/dev/null || true
EOF
chmod +x "$E2E_RUNTIME/cleanup-netns.sh"
