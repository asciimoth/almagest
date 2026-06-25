#!/usr/bin/env bash
set -euo pipefail

command -v curl >/dev/null
command -v ip >/dev/null
command -v jq >/dev/null
command -v python3 >/dev/null
test -c /dev/net/tun

local_ns="alm-e2e-local"
router_ns="alm-e2e-router"
public_ns="alm-e2e-public"

cleanup() {
	ip netns del "$local_ns" 2>/dev/null || true
	ip netns del "$router_ns" 2>/dev/null || true
	ip netns del "$public_ns" 2>/dev/null || true
	ip link del alml0 2>/dev/null || true
	ip link del almr0 2>/dev/null || true
	ip route del 203.0.113.0/24 via 10.213.2.1 dev almr0 2>/dev/null || true
}

cleanup

if ! ip netns add "$local_ns" 2>/tmp/e2e-netns.err; then
	echo "default_tun_routing requires privileged Docker networking" >&2
	echo 'run with E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun"' >&2
	cat /tmp/e2e-netns.err >&2
	exit 1
fi
ip netns add "$router_ns"
ip netns add "$public_ns"

ip link add alml0 type veth peer name alml1
ip link set alml1 netns "$local_ns"
ip addr add 10.213.1.1/24 dev alml0
ip link set alml0 up
ip -n "$local_ns" addr add 10.213.1.2/24 dev alml1
ip -n "$local_ns" link set lo up
ip -n "$local_ns" link set alml1 up

ip link add almr0 type veth peer name almr1
ip link set almr1 netns "$router_ns"
ip addr add 10.213.2.2/24 dev almr0
ip link set almr0 up
ip -n "$router_ns" addr add 10.213.2.1/24 dev almr1
ip -n "$router_ns" link set lo up
ip -n "$router_ns" link set almr1 up

ip -n "$router_ns" link add almp0 type veth peer name almp1
ip -n "$router_ns" link set almp1 netns "$public_ns"
ip -n "$router_ns" addr add 203.0.113.1/24 dev almp0
ip -n "$router_ns" link set almp0 up
ip -n "$public_ns" addr add 203.0.113.2/24 dev almp1
ip -n "$public_ns" link set lo up
ip -n "$public_ns" link set almp1 up
ip -n "$public_ns" route add default via 203.0.113.1 dev almp1

ip netns exec "$router_ns" sysctl -qw net.ipv4.ip_forward=1
ip route add 203.0.113.0/24 via 10.213.2.1 dev almr0

cat >"$E2E_RUNTIME/cleanup-netns.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
ip route del 203.0.113.0/24 via 10.213.2.1 dev almr0 2>/dev/null || true
ip netns del alm-e2e-local 2>/dev/null || true
ip netns del alm-e2e-router 2>/dev/null || true
ip netns del alm-e2e-public 2>/dev/null || true
ip link del alml0 2>/dev/null || true
ip link del almr0 2>/dev/null || true
EOF
chmod +x "$E2E_RUNTIME/cleanup-netns.sh"
