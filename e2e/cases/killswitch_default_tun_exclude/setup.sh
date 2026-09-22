#!/usr/bin/env bash
set -euo pipefail

command -v curl >/dev/null
command -v ip >/dev/null
command -v jq >/dev/null
command -v killswitch >/dev/null
command -v killswitch-cli >/dev/null
command -v mount >/dev/null
command -v python3 >/dev/null
test -c /dev/net/tun

cp "$(command -v curl)" /usr/local/bin/excluded-curl
cp "$(command -v python3)" /usr/local/bin/pmark-target
chmod 0755 /usr/local/bin/excluded-curl /usr/local/bin/pmark-target

router_ns="alm-e2e-ks-router"
target_ns="alm-e2e-ks-target"
root_if="almks0"

cleanup() {
	ip route del 203.0.114.0/24 via 10.214.2.1 dev "$root_if" 2>/dev/null || true
	ip netns del "$router_ns" 2>/dev/null || true
	ip netns del "$target_ns" 2>/dev/null || true
	ip link del "$root_if" 2>/dev/null || true
	rm -rf /sys/fs/bpf/almagest-e2e-killswitch-default-tun-exclude 2>/dev/null || true
	if [[ -f "$E2E_RUNTIME/mounted-bpffs" ]]; then
		umount /sys/fs/bpf 2>/dev/null || true
	fi
}

cleanup

mkdir -p /sys/fs/bpf
if ! awk '$2 == "/sys/fs/bpf" && $3 == "bpf" { found=1 } END { exit !found }' /proc/mounts; then
	if ! mount -t bpf bpffs /sys/fs/bpf 2>/tmp/e2e-bpffs.err; then
		echo "killswitch_default_tun_exclude requires bpffs mount support" >&2
		cat /tmp/e2e-bpffs.err >&2
		exit 1
	fi
	touch "$E2E_RUNTIME/mounted-bpffs"
fi

if ! ip netns add "$router_ns" 2>/tmp/e2e-netns.err; then
	echo "killswitch_default_tun_exclude requires privileged Docker networking" >&2
	echo 'run with E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun"' >&2
	cat /tmp/e2e-netns.err >&2
	exit 1
fi
ip netns add "$target_ns"

ip link add "$root_if" type veth peer name almks1
ip link set almks1 netns "$router_ns"
ip addr add 10.214.2.2/24 dev "$root_if"
ip link set "$root_if" up
ip -n "$router_ns" addr add 10.214.2.1/24 dev almks1
ip -n "$router_ns" link set lo up
ip -n "$router_ns" link set almks1 up

ip -n "$router_ns" link add almksr0 type veth peer name almksr1
ip -n "$router_ns" link set almksr1 netns "$target_ns"
ip -n "$router_ns" addr add 203.0.114.1/24 dev almksr0
ip -n "$router_ns" link set almksr0 up
ip -n "$target_ns" addr add 203.0.114.2/24 dev almksr1
ip -n "$target_ns" link set lo up
ip -n "$target_ns" link set almksr1 up
ip -n "$target_ns" route add default via 203.0.114.1 dev almksr1

ip netns exec "$router_ns" sysctl -qw net.ipv4.ip_forward=1
ip route add 203.0.114.0/24 via 10.214.2.1 dev "$root_if"

sed -i "s@%E2E_RUNTIME%@$E2E_RUNTIME@g" protected_alm.hujson

cat >"$E2E_RUNTIME/killswitch.json" <<EOF
{
  "interface_names": ["$root_if"],
  "admin_api": {
    "socket_path": "$E2E_RUNTIME/killswitch.sock",
    "auth": {
      "uids": [0]
    }
  },
  "allow_all": false,
  "enable_v4": true,
  "enable_v6": false,
  "allowed_marks": [],
  "allowed_ports": [],
  "allowed_v4_hosts": [],
  "allowed_v6_hosts": [],
  "allowed_v4_hostports": [],
  "allowed_v6_hostports": []
}
EOF

cat >"$E2E_RUNTIME/cleanup-netns.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
ip route del 203.0.114.0/24 via 10.214.2.1 dev almks0 2>/dev/null || true
ip netns del alm-e2e-ks-router 2>/dev/null || true
ip netns del alm-e2e-ks-target 2>/dev/null || true
ip link del almks0 2>/dev/null || true
rm -rf /sys/fs/bpf/almagest-e2e-killswitch-default-tun-exclude 2>/dev/null || true
if [[ -f /tmp/almagest-e2e/killswitch_default_tun_exclude/mounted-bpffs ]]; then
	umount /sys/fs/bpf 2>/dev/null || true
fi
EOF
chmod +x "$E2E_RUNTIME/cleanup-netns.sh"
