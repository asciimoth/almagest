#!/usr/bin/env bash
set -euo pipefail

command -v jq >/dev/null
command -v ip >/dev/null
command -v wg >/dev/null

ip addr show lo >/dev/null

strip_padding() {
	printf '%s' "${1%=}"
}

assert_padded_key() {
	local name="$1"
	local value="$2"
	if [[ ${#value} -ne 44 || "$value" != *= ]]; then
		echo "$name must be a 44-character padded wg key, got ${#value}: $value" >&2
		exit 1
	fi
}

assert_unpadded_key() {
	local name="$1"
	local value="$2"
	if [[ ${#value} -ne 43 || "$value" == *= ]]; then
		echo "$name must be a 43-character unpadded wg key, got ${#value}: $value" >&2
		exit 1
	fi
}

client_private="$(wg genkey)"
client_public="$(printf '%s\n' "$client_private" | wg pubkey)"
server_private_padded="$(wg genkey)"
server_public_padded="$(printf '%s\n' "$server_private_padded" | wg pubkey)"
preshared_key_padded="$(wg genpsk)"

server_private="$(strip_padding "$server_private_padded")"
server_public="$(strip_padding "$server_public_padded")"
preshared_key="$(strip_padding "$preshared_key_padded")"

assert_padded_key client_private "$client_private"
assert_padded_key client_public "$client_public"
assert_padded_key server_private_padded "$server_private_padded"
assert_padded_key server_public_padded "$server_public_padded"
assert_padded_key preshared_key_padded "$preshared_key_padded"
assert_unpadded_key server_private "$server_private"
assert_unpadded_key server_public "$server_public"
assert_unpadded_key preshared_key "$preshared_key"

cat >wg-keys.env <<EOF
CLIENT_PRIVATE='$client_private'
CLIENT_PUBLIC='$client_public'
SERVER_PRIVATE_PADDED='$server_private_padded'
SERVER_PRIVATE='$server_private'
SERVER_PUBLIC_PADDED='$server_public_padded'
SERVER_PUBLIC='$server_public'
PRESHARED_KEY_PADDED='$preshared_key_padded'
PRESHARED_KEY='$preshared_key'
EOF

cat >alm.hujson <<EOF
{
	"Logs": {
		"Cache": {
			"Debug": 512,
			"Info": 128,
			"Warn": 128,
			"Error": 128,
			"Fatal": 32,
		},
	},
	"nodes": {
		"WireGuard CLI Client": {
			"Class":      "almagest/WireGuard",
			"Pos":        "{\"x\":488,\"y\":180}",
			"Links":      ["Network -> [SocksClient CLI Client] Network"],
			"privateKey": "$client_private",
			"listenPort": 51840,
			"ownedIPs":   "",
			"peers": [{
				"allowedIPs":                  "10.199.20.1/32",
				"endpoint":                    "127.0.0.1:51841",
				"id":                          "server",
				"persistentKeepaliveInterval": 1,
				"presharedKey":                "$preshared_key",
				"publicKey":                   "$server_public",
			}],
		},

		"WireGuard CLI Server": {
			"Class":      "almagest/WireGuard",
			"Pos":        "{\"x\":476,\"y\":377}",
			"Links":      ["Network -> [SocksClient CLI Server] Network"],
			"privateKey": "$server_private",
			"listenPort": 51841,
			"ownedIPs":   "",
			"peers": [{
				"allowedIPs":                  "10.199.10.1/32",
				"endpoint":                    "127.0.0.1:51840",
				"id":                          "client",
				"persistentKeepaliveInterval": 1,
				"presharedKey":                "$preshared_key",
				"publicKey":                   "$client_public",
			}],
		},

		"VTun CLI Client": {
			"Class":          "almagest/VTun",
			"Pos":            "{\"x\":292,\"y\":186}",
			"Links":          ["Tun -> [WireGuard CLI Client] Tun"],
			"endpointCh":     1024,
			"eventCh":        10,
			"noLoopbackAddr": false,
			"disableSACK":    false,
			"name":           "wg-cli-client",
			"localAddrs":     "10.199.10.1/24",
			"dnsServers":     "",
			"mtu":            1420,
		},

		"VTun CLI Server": {
			"Class":          "almagest/VTun",
			"Pos":            "{\"x\":295,\"y\":381}",
			"Links":          ["Tun -> [WireGuard CLI Server] Tun"],
			"eventCh":        10,
			"noLoopbackAddr": false,
			"disableSACK":    false,
			"name":           "wg-cli-server",
			"localAddrs":     "10.199.20.1/24",
			"dnsServers":     "",
			"mtu":            1420,
			"endpointCh":     1024,
		},

		"HTTP CLI Server": {
			"Class":    "almagest/HttpServer",
			"Pos":      "{\"x\":98,\"y\":374}",
			"Links":    ["Network -> [VTun CLI Server] Network"],
			"host":     "10.199.20.1",
			"port":     18081,
			"response": "wg cli key compatibility",
		},

		"HTTP CLI Client": {
			"Class":  "almagest/HttpClient",
			"Pos":    "{\"x\":105,\"y\":185}",
			"Links":  ["Network -> [VTun CLI Client] Network"],
			"url":    "http://10.199.20.1:18081/",
			"method": "GET",
			"body":   "",
		},
		"SocksClient CLI Client": {
			"Class":    "almagest/SocksClient",
			"Pos":      "{\"x\":701,\"y\":178}",
			"Links":    ["out -> [OutNetwork CLI Shared] Network"],
			"proxyURL": "socks5://127.0.0.1:1082?gost",
		},
		"SocksServer CLI": {
			"Class": "almagest/SocksServer",
			"Pos":   "{\"x\":742,\"y\":499}",
			"Links": ["listen -> [OutNetwork CLI Shared] Network", "proxy -> [OutNetwork CLI Proxy] Network"],
			"users": [],
			"commands": {
				"cmdBind":          true,
				"cmdConnect":       true,
				"cmdGostMuxBind":   true,
				"cmdGostUDPTun":    true,
				"cmdTorResolve":    true,
				"cmdTorResolvePtr": true,
				"cmdUDPAssoc":      true,
			},
			"socksHost": "127.0.0.1",
			"socksPort": 1082,
			"wsHost":    "",
			"wsPort":    0,
			"wsPath":    "/socks",
		},
		"SocksClient CLI Server": {
			"Class":    "almagest/SocksClient",
			"Pos":      "{\"x\":688,\"y\":354}",
			"Links":    ["out -> [OutNetwork CLI Shared] Network"],
			"proxyURL": "socks5://127.0.0.1:1082?gost",
		},
		"OutNetwork CLI Shared": {"Class": "almagest/OutNetwork", "Pos": "{\"x\":1042,\"y\":296}"},
		"OutNetwork CLI Proxy": {"Class": "almagest/OutNetwork", "Pos": "{\"x\":1036,\"y\":537}"},
	},
}
EOF
