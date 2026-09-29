#!/usr/bin/env bash
set -euo pipefail

command -v curl >/dev/null
command -v ip >/dev/null
command -v jq >/dev/null
test -c /dev/net/tun

pin_path="/sys/fs/bpf/almagest-e2e-tun-routing"
upstream_ns="alm-tun-upstream"
upstream_main_if="almtu0"
upstream_peer_if="almtu1"
upstream_gateway="192.0.2.1"
upstream_client="192.0.2.2"
upstream_server="198.51.100.1"
upstream_port="18080"

cleanup() {
	local server_pid=""
	if [[ -r "$E2E_RUNTIME/default-route-server.pid" ]]; then
		read -r server_pid <"$E2E_RUNTIME/default-route-server.pid" || true
	fi
	if [[ -n "$server_pid" ]]; then
		kill "$server_pid" 2>/dev/null || true
	fi
	ip netns del "$upstream_ns" 2>/dev/null || true
	ip link del "$upstream_main_if" 2>/dev/null || true
	ip link del tun1 2>/dev/null || true
	ip link del tun2 2>/dev/null || true
	ip link del alm0 2>/dev/null || true
	ip link del alm1 2>/dev/null || true
	ip link del alme2eprobe 2>/dev/null || true
	rm -rf "$pin_path" 2>/dev/null || true
}

cleanup

if ! ip tuntap add dev alme2eprobe mode tun 2>/tmp/e2e-tun.err; then
	echo "tun_routing requires privileged Docker networking" >&2
	echo 'run with E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun" ./e2e/run.sh tun_routing' >&2
	cat /tmp/e2e-tun.err >&2
	exit 1
fi
ip link del alme2eprobe

# Use a local network namespace as the ordinary upstream. The probe address is
# reachable only through this container's default route. This keeps the route
# lifecycle test deterministic and does not depend on a public HTTP service.
ip netns add "$upstream_ns"
ip link add "$upstream_main_if" type veth peer name "$upstream_peer_if"
ip link set "$upstream_peer_if" netns "$upstream_ns"
ip addr add "$upstream_client/30" dev "$upstream_main_if"
ip link set "$upstream_main_if" up
ip -n "$upstream_ns" addr add "$upstream_gateway/30" dev "$upstream_peer_if"
ip -n "$upstream_ns" addr add "$upstream_server/32" dev lo
ip -n "$upstream_ns" link set lo up
ip -n "$upstream_ns" link set "$upstream_peer_if" up
printf '%s\n' 'ordinary default route' >"$E2E_RUNTIME/default-route-probe"
ip netns exec "$upstream_ns" python3 -m http.server "$upstream_port" \
	--bind "$upstream_server" --directory "$E2E_RUNTIME" \
	>"$E2E_RUNTIME/default-route-server.log" 2>&1 &
echo "$!" >"$E2E_RUNTIME/default-route-server.pid"
for _ in $(seq 1 50); do
	if ip netns exec "$upstream_ns" curl --fail --silent \
		"http://$upstream_server:$upstream_port/default-route-probe" \
		>/dev/null; then
		break
	fi
	sleep 0.05
done
if ! ip netns exec "$upstream_ns" curl --fail --silent --show-error \
	"http://$upstream_server:$upstream_port/default-route-probe" \
	>/dev/null; then
	cat "$E2E_RUNTIME/default-route-server.log" >&2
	exit 1
fi
ip route replace default via "$upstream_gateway" dev "$upstream_main_if"

cat >"$E2E_RUNTIME/cleanup-tun.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
server_pid=""
if [[ -r /tmp/almagest-e2e/tun_routing/default-route-server.pid ]]; then
	read -r server_pid </tmp/almagest-e2e/tun_routing/default-route-server.pid || true
fi
if [[ -n "$server_pid" ]]; then
	kill "$server_pid" 2>/dev/null || true
fi
ip netns del alm-tun-upstream 2>/dev/null || true
ip link del almtu0 2>/dev/null || true
ip link del tun1 2>/dev/null || true
ip link del tun2 2>/dev/null || true
ip link del alm0 2>/dev/null || true
ip link del alm1 2>/dev/null || true
rm -rf /sys/fs/bpf/almagest-e2e-tun-routing 2>/dev/null || true
EOF
chmod +x "$E2E_RUNTIME/cleanup-tun.sh"
