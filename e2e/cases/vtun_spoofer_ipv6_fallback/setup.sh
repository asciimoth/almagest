#!/usr/bin/env bash
set -euo pipefail

for command in dig dnsmasq ip jq nc nft python3; do
	command -v "$command" >/dev/null
done

blackhole_ns="alm-ds-blackhole"
host_link="alm-ds-host"
peer_link="alm-ds-peer"

cleanup() {
	ip link del "$host_link" 2>/dev/null || true
	ip netns del "$blackhole_ns" 2>/dev/null || true
}

cleanup
if ! ip netns add "$blackhole_ns" 2>"$E2E_RUNTIME/netns.err"; then
	echo "vtun_spoofer_ipv6_fallback requires privileged Docker networking" >&2
	echo 'run with E2E_DOCKER_RUN_ARGS="--privileged" ./e2e/run.sh vtun_spoofer_ipv6_fallback' >&2
	cat "$E2E_RUNTIME/netns.err" >&2
	exit 1
fi

ip link add "$host_link" type veth peer name "$peer_link"
ip link set "$peer_link" netns "$blackhole_ns"
ip addr add 192.0.2.53/32 dev "$host_link"
ip addr add 192.0.2.80/32 dev "$host_link"
ip -6 addr add 2001:db8:dead::1/64 dev "$host_link" nodad
ip link set "$host_link" up
ip -n "$blackhole_ns" link set lo up
ip -n "$blackhole_ns" -6 addr add 2001:db8:dead::80/64 dev "$peer_link" nodad
ip -n "$blackhole_ns" link set "$peer_link" up

ip netns exec "$blackhole_ns" nft -f - <<'EOF'
table ip6 almagest_e2e {
	chain input {
		type filter hook input priority 0; policy accept;
		tcp dport 18080 counter drop
	}
}
EOF

cat >"$E2E_RUNTIME/cleanup-host.sh" <<EOF
#!/usr/bin/env bash
set -euo pipefail
ip link del "$host_link" 2>/dev/null || true
ip netns del "$blackhole_ns" 2>/dev/null || true
EOF
chmod +x "$E2E_RUNTIME/cleanup-host.sh"
