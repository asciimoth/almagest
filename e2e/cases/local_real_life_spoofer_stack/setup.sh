#!/usr/bin/env bash
set -euo pipefail

# The case runner starts two origins and a combined Headscale, DNS, and
# tailnet-peer service before it starts this main container.
: "${E2E_HEADSCALE_IP:?}"
: "${E2E_PUBLIC_IP:?}"
: "${E2E_EXAMPLE_IP:?}"
: "${E2E_INTERNET_GATEWAY_IP:?}"

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

# Remove the connected route for the fixture bridge. This makes normal
# process traffic eligible for DefaultTun. Keep a route to the Docker gateway
# so excluded processes and Spoofer's host sockets retain direct egress.
internet_if="$(ip -4 route get "$E2E_PUBLIC_IP" | awk '{for (i=1; i<=NF; i++) if ($i == "dev") {print $(i+1); exit}}')"
internet_prefix="$(ip -4 route show dev "$internet_if" proto kernel | awk 'NR == 1 {print $1}')"
[[ -n "$internet_if" && -n "$internet_prefix" ]]
ip route replace "$E2E_INTERNET_GATEWAY_IP/32" dev "$internet_if"
ip route del "$internet_prefix" dev "$internet_if"
while ip route del default 2>/dev/null; do :; done
ip route add default via "$E2E_INTERNET_GATEWAY_IP" dev "$internet_if"

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
rm -rf /sys/fs/bpf/almagest-e2e-local-real-life-spoofer-stack 2>/dev/null || true
if [[ -f /tmp/almagest-e2e/local_real_life_spoofer_stack/mounted-bpffs ]]; then
	umount /sys/fs/bpf 2>/dev/null || true
fi
EOF
chmod 0755 "$E2E_RUNTIME/cleanup-system.sh"
