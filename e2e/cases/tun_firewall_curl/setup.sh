#!/usr/bin/env bash
set -euo pipefail

command -v curl >/dev/null
command -v ip >/dev/null
command -v jq >/dev/null
test -c /dev/net/tun

pin_path="/sys/fs/bpf/almagest-e2e-tun-firewall-curl"

cleanup() {
	ip link del almfw0 2>/dev/null || true
	ip link del alme2eprobe 2>/dev/null || true
	rm -rf "$pin_path" 2>/dev/null || true
}

cleanup

if ! ip tuntap add dev alme2eprobe mode tun 2>/tmp/e2e-tun-firewall.err; then
	echo "tun_firewall_curl requires privileged Docker networking" >&2
	echo 'run with E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun" ./e2e/run.sh tun_firewall_curl' >&2
	cat /tmp/e2e-tun-firewall.err >&2
	exit 1
fi
ip link del alme2eprobe

cat >"$E2E_RUNTIME/cleanup-tun-firewall.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
ip link del almfw0 2>/dev/null || true
rm -rf /sys/fs/bpf/almagest-e2e-tun-firewall-curl 2>/dev/null || true
EOF
chmod +x "$E2E_RUNTIME/cleanup-tun-firewall.sh"

