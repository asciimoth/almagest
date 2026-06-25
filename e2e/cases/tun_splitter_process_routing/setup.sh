#!/usr/bin/env bash
set -euo pipefail

command -v curl >/dev/null
command -v ip >/dev/null
command -v jq >/dev/null
command -v mount >/dev/null
command -v python3 >/dev/null
command -v wget >/dev/null
test -c /dev/net/tun

router_ns="alm-e2e-ts-router"
public_ns="alm-e2e-ts-public"
root_if="almts0"
pin_path="/sys/fs/bpf/almagest-e2e-tun-splitter-process-routing"

cleanup() {
	ip route del 203.0.115.0/24 via 10.215.2.1 dev "$root_if" 2>/dev/null || true
	ip netns del "$router_ns" 2>/dev/null || true
	ip netns del "$public_ns" 2>/dev/null || true
	ip link del "$root_if" 2>/dev/null || true
	rm -rf "$pin_path" 2>/dev/null || true
	if [[ -f "$E2E_RUNTIME/mounted-bpffs" ]]; then
		umount /sys/fs/bpf 2>/dev/null || true
	fi
}

cleanup

mkdir -p /sys/fs/bpf
if ! awk '$2 == "/sys/fs/bpf" && $3 == "bpf" { found=1 } END { exit !found }' /proc/mounts; then
	if ! mount -t bpf bpffs /sys/fs/bpf 2>/tmp/e2e-bpffs.err; then
		echo "tun_splitter_process_routing requires bpffs mount support" >&2
		cat /tmp/e2e-bpffs.err >&2
		exit 1
	fi
	touch "$E2E_RUNTIME/mounted-bpffs"
fi

if ! ip netns add "$router_ns" 2>/tmp/e2e-netns.err; then
	echo "tun_splitter_process_routing requires privileged Docker networking" >&2
	echo 'run with E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun"' >&2
	cat /tmp/e2e-netns.err >&2
	exit 1
fi
ip netns add "$public_ns"

ip link add "$root_if" type veth peer name almts1
ip link set almts1 netns "$router_ns"
ip addr add 10.215.2.2/24 dev "$root_if"
ip link set "$root_if" up
ip -n "$router_ns" addr add 10.215.2.1/24 dev almts1
ip -n "$router_ns" link set lo up
ip -n "$router_ns" link set almts1 up

ip -n "$router_ns" link add almtsp0 type veth peer name almtsp1
ip -n "$router_ns" link set almtsp1 netns "$public_ns"
ip -n "$router_ns" addr add 203.0.115.1/24 dev almtsp0
ip -n "$router_ns" link set almtsp0 up
ip -n "$public_ns" addr add 203.0.115.2/24 dev almtsp1
ip -n "$public_ns" link set lo up
ip -n "$public_ns" link set almtsp1 up
ip -n "$public_ns" route add default via 203.0.115.1 dev almtsp1

ip netns exec "$router_ns" sysctl -qw net.ipv4.ip_forward=1
ip route add 203.0.115.0/24 via 10.215.2.1 dev "$root_if"

cat >"$E2E_RUNTIME/cleanup-netns.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
ip route del 203.0.115.0/24 via 10.215.2.1 dev almts0 2>/dev/null || true
ip netns del alm-e2e-ts-router 2>/dev/null || true
ip netns del alm-e2e-ts-public 2>/dev/null || true
ip link del almts0 2>/dev/null || true
rm -rf /sys/fs/bpf/almagest-e2e-tun-splitter-process-routing 2>/dev/null || true
if [[ -f /tmp/almagest-e2e/tun_splitter_process_routing/mounted-bpffs ]]; then
	umount /sys/fs/bpf 2>/dev/null || true
fi
EOF
chmod +x "$E2E_RUNTIME/cleanup-netns.sh"
