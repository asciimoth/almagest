#!/usr/bin/env bash
set -euo pipefail

for command in curl ip jq python3; do
	command -v "$command" >/dev/null
done
test -c /dev/net/tun

ethernet_ns="alm-mig-ethernet"
wifi_ns="alm-mig-wifi"

cleanup() {
	ip netns del "$ethernet_ns" 2>/dev/null || true
	ip netns del "$wifi_ns" 2>/dev/null || true
	ip link del almeth0 2>/dev/null || true
	ip link del almwifi0 2>/dev/null || true
}

cleanup

if ! ip netns add "$ethernet_ns" 2>"$E2E_RUNTIME/netns.err"; then
	echo "out_network_migration requires privileged Docker networking" >&2
	echo 'run with E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun"' >&2
	cat "$E2E_RUNTIME/netns.err" >&2
	exit 1
fi
ip netns add "$wifi_ns"

ip link add almeth0 type veth peer name almeth1
ip link set almeth1 netns "$ethernet_ns"
ip addr add 198.51.100.1/24 dev almeth0
ip link set almeth0 up
ip -n "$ethernet_ns" link set lo up
ip -n "$ethernet_ns" addr add 198.51.100.2/24 dev almeth1
ip -n "$ethernet_ns" link set almeth1 up
ip -n "$ethernet_ns" addr add 192.0.2.10/32 dev lo
ip -n "$ethernet_ns" addr add 10.210.0.10/32 dev lo

ip link add almwifi0 type veth peer name almwifi1
ip link set almwifi1 netns "$wifi_ns"
ip addr add 203.0.113.1/24 dev almwifi0
ip link set almwifi0 up
ip -n "$wifi_ns" link set lo up
ip -n "$wifi_ns" addr add 203.0.113.2/24 dev almwifi1
ip -n "$wifi_ns" link set almwifi1 up
ip -n "$wifi_ns" addr add 192.0.2.10/32 dev lo
ip -n "$wifi_ns" addr add 10.210.0.10/32 dev lo

while ip route del default 2>/dev/null; do :; done
ip route add default via 198.51.100.2 dev almeth0 onlink
ip route add 10.210.0.0/24 via 198.51.100.2 dev almeth0 onlink
ip link set almwifi0 down

cat >"$E2E_RUNTIME/cleanup-netns.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
ip netns del alm-mig-ethernet 2>/dev/null || true
ip netns del alm-mig-wifi 2>/dev/null || true
ip link del almeth0 2>/dev/null || true
ip link del almwifi0 2>/dev/null || true
EOF
chmod +x "$E2E_RUNTIME/cleanup-netns.sh"
