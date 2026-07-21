#!/usr/bin/env bash
set -euo pipefail

command -v curl >/dev/null
command -v dig >/dev/null
command -v dnsmasq >/dev/null
command -v ip >/dev/null
command -v jq >/dev/null
command -v nc >/dev/null
test -c /dev/net/tun

resolv_backup="$E2E_RUNTIME/resolv.conf.original"

cleanup() {
	ip link del almdnsprobe 2>/dev/null || true
	ip link del alm0 2>/dev/null || true
	ip link del alm1 2>/dev/null || true
}

cleanup

if ! ip tuntap add dev almdnsprobe mode tun 2>"$E2E_RUNTIME/tun-probe.err"; then
	echo "dns_example_graph requires privileged Docker networking" >&2
	echo 'run with E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun" ./e2e/run.sh dns_example_graph' >&2
	cat "$E2E_RUNTIME/tun-probe.err" >&2
	exit 1
fi
ip link del almdnsprobe

cp -a /etc/resolv.conf "$resolv_backup"

# Force sysnet-linux direct DNS to use the protected fallback server instead of
# any resolver inherited from the Docker host.
cat >/etc/resolv.conf <<'EOF'
options timeout:1 attempts:1
EOF

cat >"$E2E_RUNTIME/cleanup-host.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ -f "$E2E_RUNTIME/resolv.conf.original" ]]; then
	cp -a "$E2E_RUNTIME/resolv.conf.original" /etc/resolv.conf
fi
ip link del alm0 2>/dev/null || true
ip link del alm1 2>/dev/null || true
EOF
chmod +x "$E2E_RUNTIME/cleanup-host.sh"
