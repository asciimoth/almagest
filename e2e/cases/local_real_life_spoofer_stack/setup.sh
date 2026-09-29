#!/usr/bin/env bash
set -euo pipefail

# The case runner starts two origins and a combined Headscale, DNS, and
# tailnet-peer service before it starts this main container.
: "${E2E_HEADSCALE_IP:?}"
: "${E2E_PUBLIC_IP:?}"
: "${E2E_EXAMPLE_IP:?}"
: "${E2E_INTERNET_GATEWAY_IP:?}"
: "${E2E_BACKEND_HEADSCALE_IP:?}"
: "${E2E_BACKEND_PUBLIC_IP:?}"
: "${E2E_BACKEND_EXAMPLE_IP:?}"

for command in curl dig ip jq openssl python3 wg wget; do
	command -v "$command" >/dev/null
done
if [[ ! -c /dev/net/tun ]]; then
	echo "/dev/net/tun is required for local_real_life_spoofer_stack" >&2
	exit 1
fi

# Pmark identifies processes after they create connections. Mount bpffs only
# inside this container and record whether this case owns the mount.
pin_path=/sys/fs/bpf/almagest-e2e-local-real-life-spoofer-stack
mkdir -p /sys/fs/bpf
if ! awk '$2 == "/sys/fs/bpf" && $3 == "bpf" { found=1 } END { exit !found }' /proc/mounts; then
	if ! mount -t bpf bpffs /sys/fs/bpf 2>"$E2E_RUNTIME/bpffs.err"; then
		echo "local_real_life_spoofer_stack requires a private bpffs mount" >&2
		cat "$E2E_RUNTIME/bpffs.err" >&2
		exit 1
	fi
	touch "$E2E_RUNTIME/mounted-bpffs"
fi
mkdir -p "$pin_path"

# Move the Docker-facing interface into a router namespace. The main namespace
# gets independent Ethernet and Wi-Fi links. Both links reach the same stable
# destinations, as two real access networks reach the same Internet services.
router_ns="alm-stack-router"
backend_if="$(ip -4 route get "$E2E_BACKEND_PUBLIC_IP" | awk '{for (i=1; i<=NF; i++) if ($i == "dev") {print $(i+1); exit}}')"
backend_cidr="$(ip -o -4 addr show dev "$backend_if" scope global | awk 'NR == 1 {print $4}')"
backend_ip="${backend_cidr%/*}"
backend_prefix="$(ip -4 route show dev "$backend_if" proto kernel | awk 'NR == 1 {print $1}')"
[[ -n "$backend_if" && -n "$backend_cidr" && -n "$backend_ip" && -n "$backend_prefix" ]]

ip netns del "$router_ns" 2>/dev/null || true
ip netns add "$router_ns"
ip link set "$backend_if" netns "$router_ns"
ip -n "$router_ns" link set lo up
ip -n "$router_ns" addr replace "$backend_cidr" dev "$backend_if"
ip -n "$router_ns" link set "$backend_if" up
ip -n "$router_ns" route replace "$backend_prefix" dev "$backend_if" src "$backend_ip"
ip -n "$router_ns" route replace default via "$E2E_INTERNET_GATEWAY_IP" \
	dev "$backend_if"

ip link add almeth0 type veth peer name almeth1
ip link set almeth1 netns "$router_ns"
ip addr add 198.18.10.1/30 dev almeth0
ip link set almeth0 up
ip -n "$router_ns" addr add 198.18.10.2/30 dev almeth1
ip -n "$router_ns" link set almeth1 up

ip link add almwifi0 type veth peer name almwifi1
ip link set almwifi1 netns "$router_ns"
ip addr add 198.18.20.1/30 dev almwifi0
ip link set almwifi0 up
ip -n "$router_ns" addr add 198.18.20.2/30 dev almwifi1
ip -n "$router_ns" link set almwifi1 up

# Nested Tailscale peers can send WireGuard packets from a different source
# port than the discovery flow. Give the main namespace a stable inbound
# address and route it through the active physical link.
ip addr add 198.18.30.1/32 dev lo
ip -n "$router_ns" route add 198.18.30.1/32 via 198.18.10.1 dev almeth1

ip netns exec "$router_ns" sysctl -qw net.ipv4.ip_forward=1
ip netns exec "$router_ns" nft -f - <<EOF
table ip migration_nat {
	chain prerouting {
		type nat hook prerouting priority -100; policy accept;
		ip daddr $E2E_PUBLIC_IP dnat to $E2E_BACKEND_PUBLIC_IP
		ip daddr $E2E_EXAMPLE_IP dnat to $E2E_BACKEND_EXAMPLE_IP
		ip daddr $E2E_HEADSCALE_IP dnat to $E2E_BACKEND_HEADSCALE_IP
		ip saddr $E2E_BACKEND_HEADSCALE_IP ip daddr $backend_ip dnat to 198.18.30.1
	}

	chain postrouting {
		type nat hook postrouting priority 100; policy accept;
		oifname "$backend_if" masquerade
	}
}
EOF

while ip route del default 2>/dev/null; do :; done
ip route add default via 198.18.10.2 dev almeth0
ip link set almwifi0 down

cat >"$E2E_CONFIG_DIR/migration-network.env" <<EOF
export E2E_ETHERNET_IF=almeth0
export E2E_ETHERNET_GATEWAY_IP=198.18.10.2
export E2E_ETHERNET_CLIENT_IP=198.18.10.1
export E2E_ETHERNET_ROUTER_IF=almeth1
export E2E_WIFI_IF=almwifi0
export E2E_WIFI_GATEWAY_IP=198.18.20.2
export E2E_WIFI_CLIENT_IP=198.18.20.1
export E2E_WIFI_ROUTER_IF=almwifi1
export E2E_ROUTER_NS=$(printf '%q' "$router_ns")
export E2E_PHYSICAL_EGRESS_IP=$(printf '%q' "$backend_ip")
EOF

for path in \
	/stack-shared/headscale-auth-key \
	/stack-shared/headscale-root.pem; do
	test -s "$path"
done

# Headscale must resolve before the graph DNS path is ready. The intercepted
# host variant exists only in /etc/hosts; the other fixture names come from
# DNS through VTun and Spoofer.
if ! grep -qE '(^|[[:space:]])headscale\.stack\.test([[:space:]]|$)' /etc/hosts; then
	printf '%s headscale.stack.test\n' "$E2E_HEADSCALE_IP" >>/etc/hosts
fi
if ! grep -qE '(^|[[:space:]])intercept-hosts\.stack\.test([[:space:]]|$)' /etc/hosts; then
	printf '%s intercept-hosts.stack.test\n' "$E2E_EXAMPLE_IP" >>/etc/hosts
fi

# Stable process names model an excluded browser and a blocked downloader.
# The browser wrapper waits until p-mark applies its DefaultTun bypass mark.
curl_path="$(command -v curl)"
cp "$(command -v python3)" /usr/local/bin/direct-fetch
cp "$curl_path" /usr/local/bin/direct-fetch-curl
cp "$curl_path" /usr/local/bin/blocked-fetch
chmod 0755 \
	/usr/local/bin/direct-fetch \
	/usr/local/bin/direct-fetch-curl \
	/usr/local/bin/blocked-fetch

sed -i \
	-e "s/%HEADSCALE_IP%/$E2E_HEADSCALE_IP/g" \
	-e "s/%PUBLIC_IP%/$E2E_PUBLIC_IP/g" \
	-e "s/%EXAMPLE_IP%/$E2E_EXAMPLE_IP/g" \
	"$E2E_CONFIG_DIR/alm.hujson"

# Keep a second graph for the compatibility phase. The main phase uses the
# automatic splitter from the checked-in graph. The second phase replaces only
# that splitter with the original manual rules. Keep this copy outside the
# configuration directory so almagestd cannot load it as an active config.
manual_config="$E2E_RUNTIME/alm-manual-splitter.hujson"
cp "$E2E_CONFIG_DIR/alm.hujson" "$manual_config"
sed -i \
	-e '/"AutoTunSplitter transport": {/,/^[[:space:]]*},$/ {
		s/"AutoTunSplitter transport"/"TunSplitter transport"/
		s@"Class": "almagest/AutoTunSplitter"@"Class": "almagest/TunSplitter"@
		/^[[:space:]]*],$/a\
\t\t\t"rules": "SNET4 100.64.0.0/10\\nSNET6 fd7a:115c:a1e0::/48\\nOR\\nSLOT 2\\n\\nTRUE\\nSLOT 1",
	}' \
	-e 's/\[AutoTunSplitter transport\]/[TunSplitter transport]/g' \
	"$manual_config"

{
	printf 'export E2E_HEADSCALE_AUTH_KEY=%q\n' "$(cat /stack-shared/headscale-auth-key)"
	printf 'export E2E_WIREGUARD_TAILSCALE_PRIVATE=%q\n' "$(wg genkey)"
} >"$E2E_CONFIG_DIR/service-secrets.env"
chmod 0600 "$E2E_CONFIG_DIR/service-secrets.env"

cat >"$E2E_RUNTIME/cleanup-system.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
ip netns del alm-stack-router 2>/dev/null || true
ip link del almeth0 2>/dev/null || true
ip link del almwifi0 2>/dev/null || true
rm -rf /sys/fs/bpf/almagest-e2e-local-real-life-spoofer-stack 2>/dev/null || true
if [[ -f /tmp/almagest-e2e/local_real_life_spoofer_stack/mounted-bpffs ]]; then
	umount /sys/fs/bpf 2>/dev/null || true
fi
EOF
chmod 0755 "$E2E_RUNTIME/cleanup-system.sh"
