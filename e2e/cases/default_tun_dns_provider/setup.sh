#!/usr/bin/env bash
set -euo pipefail

command -v dnsmasq >/dev/null
command -v getent >/dev/null
command -v ip >/dev/null
command -v jq >/dev/null
command -v nc >/dev/null
command -v python3 >/dev/null
test -c /dev/net/tun

resolv_backup="$E2E_RUNTIME/resolv.conf.original"
hosts_backup="$E2E_RUNTIME/hosts.original"
router_ns="alm-dns-router"
public_ns="alm-dns-public"

mkdir -p "$E2E_RUNTIME"
cp -a /etc/resolv.conf "$resolv_backup"
cp -a /etc/hosts "$hosts_backup"

# The direct provider must capture this server before DefaultTun replaces the
# system resolver.
cat >/etc/resolv.conf <<'EOF'
nameserver 127.0.0.54
options timeout:1 attempts:1
EOF

cat >>/etc/hosts <<'EOF'
203.0.113.3 hosts-static.e2e.test hosts-alias.e2e.test HOSTS-CASE.E2E.TEST. # E2E aliases
203.0.113.3 hosts-precedence.e2e.test
::1 hosts-ipv6.e2e.test
not-an-address ignored-invalid.e2e.test
EOF

ip netns del "$router_ns" 2>/dev/null || true
ip netns del "$public_ns" 2>/dev/null || true
ip link del almdr0 2>/dev/null || true
ip route del 203.0.113.0/24 via 10.214.2.1 dev almdr0 2>/dev/null || true

if ! ip netns add "$router_ns" 2>"$E2E_RUNTIME/netns.err"; then
	echo "default_tun_dns_provider requires privileged Docker networking" >&2
	echo 'run with E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun"' >&2
	cat "$E2E_RUNTIME/netns.err" >&2
	exit 1
fi
ip netns add "$public_ns"

ip link add almdr0 type veth peer name almdr1
ip link set almdr1 netns "$router_ns"
ip addr add 10.214.2.2/24 dev almdr0
ip link set almdr0 up
ip -n "$router_ns" addr add 10.214.2.1/24 dev almdr1
ip -n "$router_ns" link set lo up
ip -n "$router_ns" link set almdr1 up

ip -n "$router_ns" link add almdp0 type veth peer name almdp1
ip -n "$router_ns" link set almdp1 netns "$public_ns"
ip -n "$router_ns" addr add 203.0.113.1/24 dev almdp0
ip -n "$router_ns" link set almdp0 up
ip -n "$public_ns" addr add 203.0.113.2/24 dev almdp1
ip -n "$public_ns" addr add 203.0.113.3/24 dev almdp1
ip -n "$public_ns" addr add 203.0.113.4/24 dev almdp1
ip -n "$public_ns" link set lo up
ip -n "$public_ns" link set almdp1 up
ip -n "$public_ns" route add default via 203.0.113.1 dev almdp1

ip netns exec "$router_ns" sysctl -qw net.ipv4.ip_forward=1
ip route add 203.0.113.0/24 via 10.214.2.1 dev almdr0

cat >"$E2E_RUNTIME/cleanup-system.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ -f "$E2E_RUNTIME/resolv.conf.original" ]]; then
	cp -a "$E2E_RUNTIME/resolv.conf.original" /etc/resolv.conf
fi
if [[ -f "$E2E_RUNTIME/hosts.original" ]]; then
	cp -a "$E2E_RUNTIME/hosts.original" /etc/hosts
fi
ip route del 203.0.113.0/24 via 10.214.2.1 dev almdr0 2>/dev/null || true
ip netns del alm-dns-router 2>/dev/null || true
ip netns del alm-dns-public 2>/dev/null || true
ip link del almdr0 2>/dev/null || true
EOF
chmod +x "$E2E_RUNTIME/cleanup-system.sh"
