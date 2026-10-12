#!/usr/bin/env bash
set -euo pipefail

command -v ip >/dev/null
command -v jq >/dev/null
command -v killswitch >/dev/null
command -v killswitch-cli >/dev/null
command -v flock >/dev/null
test -c /dev/net/tun

root_if="almcb0"
peer_if="almcb1"

cleanup() {
	ip link del "$root_if" 2>/dev/null || true
	ip link del "$peer_if" 2>/dev/null || true
	ip link del cbtun0 2>/dev/null || true
	ip link del alm0 2>/dev/null || true
}

cleanup

if ! ip link add "$root_if" type veth peer name "$peer_if" 2>/tmp/e2e-sysnet-callbacks-veth.err; then
	echo "sysnet_linux_callbacks requires privileged Docker networking" >&2
	echo 'run with E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun"' >&2
	cat /tmp/e2e-sysnet-callbacks-veth.err >&2
	exit 1
fi
ip link set "$root_if" up
ip link set "$peer_if" up

sed -i "s@%E2E_RUNTIME%@$E2E_RUNTIME@g" protected_alm.hujson

mkdir -p "$E2E_ISOLATION_ROOT/helper/callbacks"
: >"$E2E_ISOLATION_ROOT/helper/callbacks/events.log"
chmod 0777 "$E2E_ISOLATION_ROOT/helper/callbacks"
chmod 0666 "$E2E_ISOLATION_ROOT/helper/callbacks/events.log"

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

cat >"$E2E_RUNTIME/cleanup-callbacks.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
ip link del almcb0 2>/dev/null || true
ip link del almcb1 2>/dev/null || true
ip link del cbtun0 2>/dev/null || true
ip link del alm0 2>/dev/null || true
EOF
chmod +x "$E2E_RUNTIME/cleanup-callbacks.sh"
