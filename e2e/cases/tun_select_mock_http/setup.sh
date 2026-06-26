#!/usr/bin/env bash
set -euo pipefail

command -v curl >/dev/null
command -v ip >/dev/null
command -v jq >/dev/null
test -c /dev/net/tun

pin_path="/sys/fs/bpf/almagest-e2e-tun-select-mock-http"

cleanup() {
	ip link del tun-select-a 2>/dev/null || true
	ip link del tun-select-b 2>/dev/null || true
	ip link del alme2eprobe 2>/dev/null || true
	rm -rf "$pin_path" 2>/dev/null || true
}

cleanup

if ! ip tuntap add dev alme2eprobe mode tun 2>/tmp/e2e-tun-select.err; then
	echo "tun_select_mock_http requires privileged Docker networking" >&2
	echo 'run with E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun" ./e2e/run.sh tun_select_mock_http' >&2
	cat /tmp/e2e-tun-select.err >&2
	exit 1
fi
ip link del alme2eprobe

cat >"$E2E_RUNTIME/cleanup-tun.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
ip link del tun-select-a 2>/dev/null || true
ip link del tun-select-b 2>/dev/null || true
rm -rf /sys/fs/bpf/almagest-e2e-tun-select-mock-http 2>/dev/null || true
EOF
chmod +x "$E2E_RUNTIME/cleanup-tun.sh"
