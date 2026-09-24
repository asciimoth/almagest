#!/usr/bin/env bash
set -euo pipefail

command -v ip >/dev/null
command -v jq >/dev/null
command -v python3 >/dev/null
test -c /dev/net/tun

pin_path="/sys/fs/bpf/almagest-e2e-tun-high-load"

cleanup() {
	ip link del almload0 2>/dev/null || true
	ip link del almload1 2>/dev/null || true
	ip link del almload2 2>/dev/null || true
	ip link del alme2eprobe 2>/dev/null || true
	rm -rf "$pin_path" 2>/dev/null || true
}

cleanup

if ! ip tuntap add dev alme2eprobe mode tun 2>"$E2E_RUNTIME/tun-probe.err"; then
	echo "tun_high_load requires privileged Docker networking" >&2
	echo 'run with E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun" ./e2e/run.sh tun_high_load' >&2
	cat "$E2E_RUNTIME/tun-probe.err" >&2
	exit 1
fi
ip link del alme2eprobe

cat >"$E2E_RUNTIME/cleanup-tun-high-load.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
ip link del almload0 2>/dev/null || true
ip link del almload1 2>/dev/null || true
ip link del almload2 2>/dev/null || true
rm -rf /sys/fs/bpf/almagest-e2e-tun-high-load 2>/dev/null || true
EOF
chmod +x "$E2E_RUNTIME/cleanup-tun-high-load.sh"
