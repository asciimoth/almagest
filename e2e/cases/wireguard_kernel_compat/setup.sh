#!/usr/bin/env bash
set -euo pipefail

command -v ip >/dev/null
command -v jq >/dev/null
command -v wg >/dev/null
test -c /dev/net/tun

almagest_private="$(wg genkey)"
almagest_public="$(printf '%s\n' "$almagest_private" | wg pubkey)"
kernel_private="$(wg genkey)"
kernel_public="$(printf '%s\n' "$kernel_private" | wg pubkey)"
preshared_key="$(wg genpsk)"

printf '%s\n' "$kernel_private" >kernel-private.key
printf '%s\n' "$preshared_key" >preshared.key
chmod 600 kernel-private.key preshared.key

cat >wireguard.env <<EOF
ALMAGEST_PUBLIC='$almagest_public'
KERNEL_PUBLIC='$kernel_public'
EOF

cat >protected_alm.hujson <<EOF
{
	"Admin": {
		"Web": 0,
		"Unix": "$E2E_ADMIN_SOCK",
	},
	"Secrets": {
		"kernel-compat-private": {
			"Value": "$almagest_private",
			"Nodes": ["WireGuard Kernel Compat"],
		},
	},
}
EOF

cat >alm.hujson <<EOF
{
	"Logs": {
		"Cache": {
			"Debug": 1024,
			"Info": 256,
			"Warn": 256,
			"Error": 128,
			"Fatal": 32,
		},
	},
	"nodes": {
		"WireGuard Kernel Compat": {
			"Class": "almagest/WireGuard",
			"Pos": "{\"x\":480,\"y\":180}",
			"Links": ["Network -> [OutNetwork Kernel Compat] Network"],
			"privateKeySecret": "kernel-compat-private",
			"listenPort": 51860,
			"ownedIPs": "",
			"peers": [{
				"id": "kernel-peer",
				"publicKey": "$kernel_public",
				"presharedKey": "$preshared_key",
				"endpoint": "127.0.0.1:51861",
				"allowedIPs": "10.205.20.1/32",
				"persistentKeepaliveInterval": 1,
			}],
		},
		"VTun Kernel Compat": {
			"Class": "almagest/VTun",
			"Pos": "{\"x\":280,\"y\":180}",
			"Links": ["Tun -> [WireGuard Kernel Compat] Tun"],
			"endpointCh": 1024,
			"eventCh": 10,
			"noLoopbackAddr": false,
			"disableSACK": false,
			"name": "wg-kernel-compat",
			"localAddrs": "10.205.10.1/24",
			"dnsServers": "",
			"mtu": 1420,
		},
		"HTTP Kernel Compat": {
			"Class": "almagest/HttpClient",
			"Pos": "{\"x\":80,\"y\":180}",
			"Links": ["Network -> [VTun Kernel Compat] Network"],
			"url": "http://10.205.20.1:18083/kernel",
			"method": "GET",
			"body": "",
		},
		"OutNetwork Kernel Compat": {
			"Class": "almagest/OutNetwork",
			"Pos": "{\"x\":700,\"y\":180}",
		},
	},
}
EOF
