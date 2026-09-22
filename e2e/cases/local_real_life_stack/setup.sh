#!/usr/bin/env bash
set -euo pipefail

# This script runs inside the main test container. The case runner has already
# started the origin, Amnesia, and Headscale containers and has connected this
# container to the control and simulated Internet bridges. All route, mount,
# host-name, and file changes below are private to the temporary containers.

# Dynamic Docker addresses are different in each run. The case runner obtains
# them from Docker and passes them to this container instead of assigning fixed
# subnets that can conflict with other E2E cases.
: "${E2E_AMNESIA_IP:?}"
: "${E2E_HEADSCALE_IP:?}"
: "${E2E_PUBLIC_IP:?}"
: "${E2E_EXAMPLE_IP:?}"
: "${E2E_INTERNET_GATEWAY_IP:?}"

# Fail before changing the container network if the case image is incomplete.
# The `wg` command also generates a new Tailscale WireGuard key for each run.
for command in curl dig ip jq openssl python3 wg wget; do
	command -v "$command" >/dev/null
done
if [[ ! -c /dev/net/tun ]]; then
	echo "/dev/net/tun is required for local_real_life_stack" >&2
	exit 1
fi

# Pmark uses pinned BPF objects to identify an application after it creates a
# connection. Mount bpffs only in this container when Docker did not supply a
# mount. The marker tells cleanup-system.sh which mount it owns and can unmount.
pin_path=/sys/fs/bpf/almagest-e2e-local-real-life-stack
mkdir -p /sys/fs/bpf
if ! awk '$2 == "/sys/fs/bpf" && $3 == "bpf" { found=1 } END { exit !found }' /proc/mounts; then
	if ! mount -t bpf bpffs /sys/fs/bpf 2>"$E2E_RUNTIME/bpffs.err"; then
		echo "local_real_life_stack requires a private bpffs mount" >&2
		cat "$E2E_RUNTIME/bpffs.err" >&2
		exit 1
	fi
	touch "$E2E_RUNTIME/mounted-bpffs"
fi
mkdir -p "$pin_path"

# Docker normally adds a connected route for the simulated Internet bridge.
# Such a route would bypass DefaultTun. Keep a host route to the Docker gateway,
# remove the connected route, and make that gateway the default route. Normal
# traffic now enters DefaultTun. Pmark-excluded traffic keeps a usable direct
# gateway and can still reach the origin containers without using the VPN.
internet_if="$(ip -4 route get "$E2E_PUBLIC_IP" | awk '{for (i=1; i<=NF; i++) if ($i == "dev") {print $(i+1); exit}}')"
internet_prefix="$(ip -4 route show dev "$internet_if" proto kernel | awk 'NR == 1 {print $1}')"
[[ -n "$internet_if" && -n "$internet_prefix" ]]
ip route replace "$E2E_INTERNET_GATEWAY_IP/32" dev "$internet_if"
ip route del "$internet_prefix" dev "$internet_if"
while ip route del default 2>/dev/null; do :; done
ip route add default via "$E2E_INTERNET_GATEWAY_IP" dev "$internet_if"

# The service containers publish these files only after they pass their active
# readiness checks. Requiring nonempty files also prevents a partial service
# startup from producing an invalid daemon configuration.
for path in \
	/stack-shared/amnesia-activation.vpn \
	/stack-shared/amnesia-gateway-public.pem \
	/stack-shared/amnesia-root.pem \
	/stack-shared/headscale-auth-key \
	/stack-shared/headscale-root.pem; do
	test -s "$path"
done

# Use one name from /etc/hosts and one name from the private DNS daemon. This
# distinction lets the traffic matrix test both local resolution and DNS-cache
# reverse lookup. The Amnesia API name must resolve before the VPN is active.
if ! grep -qE '(^|[[:space:]])intercept-hosts\.stack\.test([[:space:]]|$)' /etc/hosts; then
	printf '%s intercept-hosts.stack.test\n' "$E2E_EXAMPLE_IP" >>/etc/hosts
fi
if ! grep -qE '(^|[[:space:]])amnesia\.stack\.test([[:space:]]|$)' /etc/hosts; then
	printf '%s amnesia.stack.test\n' "$E2E_AMNESIA_IP" >>/etc/hosts
fi

# Give the test clients stable process names. `direct-fetch` waits until p-mark
# applies the DefaultTun bypass mark, then starts `direct-fetch-curl`. Both names
# match the exclusion rule, so the curl child keeps the inherited mark after
# exec. `blocked-fetch` represents an application that TunSplitter must reject.
curl_path="$(command -v curl)"
cp "$(command -v python3)" /usr/local/bin/direct-fetch
cp "$curl_path" /usr/local/bin/direct-fetch-curl
cp "$curl_path" /usr/local/bin/blocked-fetch
chmod 0755 \
	/usr/local/bin/direct-fetch \
	/usr/local/bin/direct-fetch-curl \
	/usr/local/bin/blocked-fetch

# Replace address tokens only in the temporary case copy. The checked-in graph
# remains portable and concurrent runs can use different Docker subnets.
sed -i \
	-e "s/%AMNESIA_IP%/$E2E_AMNESIA_IP/g" \
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
	-e '/"AutoTunSplitter VPN": {/,/^[[:space:]]*},$/ {
		s/"AutoTunSplitter VPN"/"TunSplitter VPN"/
		s@"Class": "almagest/AutoTunSplitter"@"Class": "almagest/TunSplitter"@
		/^[[:space:]]*],$/a\
\t\t\t"rules": "SNET4 100.64.0.0/10\\nSNET6 fd7a:115c:a1e0::/48\\nOR\\nSLOT 2\\n\\nTRUE\\nSLOT 1",
	}' \
	-e 's/\[AutoTunSplitter VPN\]/[TunSplitter VPN]/g' \
	"$manual_config"

# Move ephemeral service credentials into environment-backed daemon secrets.
# The Amnesia and Headscale containers create their values for this run. The
# main Tailscale WireGuard key is also generated here. No private key from a
# real system, or reusable test private key, is stored in the repository.
{
	printf 'export E2E_AMNESIA_ACTIVATION=%q\n' "$(cat /stack-shared/amnesia-activation.vpn)"
	printf 'export E2E_AMNESIA_GATEWAY_PUBLIC=%q\n' "$(cat /stack-shared/amnesia-gateway-public.pem)"
	printf 'export E2E_HEADSCALE_AUTH_KEY=%q\n' "$(cat /stack-shared/headscale-auth-key)"
	printf 'export E2E_WIREGUARD_TAILSCALE_PRIVATE=%q\n' "$(wg genkey)"
} >"$E2E_CONFIG_DIR/service-secrets.env"
chmod 0600 "$E2E_CONFIG_DIR/service-secrets.env"

# The common E2E lifecycle calls this script after it stops almagestd. Remove
# only this case's BPF pins. Unmount bpffs only when setup.sh mounted it above.
cat >"$E2E_RUNTIME/cleanup-system.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
rm -rf /sys/fs/bpf/almagest-e2e-local-real-life-stack 2>/dev/null || true
if [[ -f /tmp/almagest-e2e/local_real_life_stack/mounted-bpffs ]]; then
	umount /sys/fs/bpf 2>/dev/null || true
fi
EOF
chmod 0755 "$E2E_RUNTIME/cleanup-system.sh"
