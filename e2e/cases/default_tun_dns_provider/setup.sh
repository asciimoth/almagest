#!/usr/bin/env bash
set -euo pipefail

command -v dnsmasq >/dev/null
command -v getent >/dev/null
command -v ip >/dev/null
command -v jq >/dev/null
command -v nc >/dev/null
test -c /dev/net/tun

resolv_backup="$E2E_RUNTIME/resolv.conf.original"

mkdir -p "$E2E_RUNTIME"
cp -a /etc/resolv.conf "$resolv_backup"

# Force sysnet-linux direct DNS to use the protected fallback server instead of
# any resolver inherited from the Docker host. The test restores the original
# file through the cleanup script below.
cat >/etc/resolv.conf <<'EOF'
options timeout:1 attempts:1
EOF

cat >"$E2E_RUNTIME/cleanup-resolv.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ -f "$E2E_RUNTIME/resolv.conf.original" ]]; then
	cp -a "$E2E_RUNTIME/resolv.conf.original" /etc/resolv.conf
fi
EOF
chmod +x "$E2E_RUNTIME/cleanup-resolv.sh"
