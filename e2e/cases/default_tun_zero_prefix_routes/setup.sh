#!/usr/bin/env bash
set -euo pipefail

command -v ip >/dev/null
command -v jq >/dev/null
test -c /dev/net/tun

router_ns="alm-zero-router"
root_if="almzr0"

ip netns del "$router_ns" 2>/dev/null || true
ip link del "$root_if" 2>/dev/null || true

if ! ip netns add "$router_ns" 2>"$E2E_RUNTIME/netns.err"; then
	echo "default_tun_zero_prefix_routes requires privileged Docker networking" >&2
	echo 'run with E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun"' >&2
	cat "$E2E_RUNTIME/netns.err" >&2
	exit 1
fi

# Docker normally supplies only an IPv4 default route. Add a disposable IPv6
# gateway so the test can verify both policy-routing families.
ip link add "$root_if" type veth peer name almzr1
ip link set almzr1 netns "$router_ns"
ip addr add 2001:db8:66::2/64 dev "$root_if"
ip link set "$root_if" up
ip -n "$router_ns" addr add 2001:db8:66::1/64 dev almzr1
ip -n "$router_ns" link set lo up
ip -n "$router_ns" link set almzr1 up
ip -6 route add default via 2001:db8:66::1 dev "$root_if"

cat >"$E2E_RUNTIME/cleanup-netns.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
ip -6 route del default via 2001:db8:66::1 dev almzr0 2>/dev/null || true
ip netns del alm-zero-router 2>/dev/null || true
ip link del almzr0 2>/dev/null || true
EOF
chmod +x "$E2E_RUNTIME/cleanup-netns.sh"
