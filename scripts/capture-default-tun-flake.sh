#!/usr/bin/env bash
# Reproduce and capture the flaky DefaultTun host-network failure.
#
# Run this script through `just capture-default-tun-flake` so Just loads the
# credential variables from .env. The script asks sudo for credentials once,
# builds the daemon and CLI, and then repeats clean daemon starts until it
# observes this specific condition:
#
#   1. All required runtime nodes are operational.
#   2. All four in-daemon probes succeed.
#   3. Multiple host curl requests fail while fresh in-daemon controls pass.
#
# When the condition occurs, the script deletes DefaultTun, creates a new
# DefaultTun with the same name, reconnects its TUN and DNS links, and repeats
# all checks. The script uses an embedded graph and a random temporary config,
# file-storage, admin-socket, and web-port setup. Detailed output goes to an
# artifact directory. The terminal shows only major progress messages.
#
# The final concise lifecycle records include the initial or replaced native
# DefaultTun source, its generation, MRO, MWO, and batch size. Terminal
# TunSplitter and TunJoiner input failures include their link and attachment
# generation. Detailed output-cache and unchanged-configuration traces were
# removed after deterministic regression tests replaced them.
#
# Important environment variables:
#   ARTIFACT_BASE       Parent output directory.
#   HOST_PROBE_URL      URL used to classify host connectivity.
#   HOST_FAILURE_CONFIRMATIONS
#                       Consecutive host failures required for classification.
#   HOST_FAILURE_CONFIRM_DELAY
#                       Delay between host failure confirmations, in seconds.
#   INITIAL_DELAY       Delay after daemon start, in seconds.
#   OPERATIONAL_TIMEOUT Maximum time to wait for required node labels.
#   SETTLE_DELAY        Extra delay after nodes become operational.
#   RECREATE_DELAY      Delay after delete and after reconnect.
#   BETWEEN_TRIES_DELAY Delay between clean tries.
#   MAX_TRIES           Zero means no limit. This is the default.
#   MAX_INCONCLUSIVE    Consecutive unrelated-failure limit. Zero has no limit.
#   CLI_TIMEOUT         Timeout for one admin API command.
#   DAEMON_BINARY      Daemon executable. The default is ./almagestd.
#   CLI_BINARY         Prebuilt CLI used when SKIP_BUILD=1.
#   SKIP_BUILD         Set to 1 to use prebuilt binaries, for example in Docker.
#
# Required credential environment variables:
#   DEFAULT_TUN_FLAKE_AMNESIA_CONNECTION_BASE64
#   DEFAULT_TUN_FLAKE_WIREGUARD_A_PRIVATE_KEY_BASE64
#   DEFAULT_TUN_FLAKE_WIREGUARD_TAILSCALE_PRIVATE_KEY_BASE64
#   DEFAULT_TUN_FLAKE_AMNESIA_INSTALLATION_UUID
#   DEFAULT_TUN_FLAKE_TAILSCALE_MZ_CACHE_BASE64
#   DEFAULT_TUN_FLAKE_TAILSCALE_CACHE_BASE64
#
# Example:
#   just capture-default-tun-flake
#   HOST_PROBE_URL=https://example.org/ INITIAL_DELAY=20 \
#     just capture-default-tun-flake

set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

artifact_base="${ARTIFACT_BASE:-$repo_root/artifacts/default-tun-flake}"
host_probe_url="${HOST_PROBE_URL:-https://example.com/}"
host_failure_confirmations="${HOST_FAILURE_CONFIRMATIONS:-3}"
host_failure_confirm_delay="${HOST_FAILURE_CONFIRM_DELAY:-2}"
initial_delay="${INITIAL_DELAY:-15}"
operational_timeout="${OPERATIONAL_TIMEOUT:-300}"
operational_stable_seconds="${OPERATIONAL_STABLE_SECONDS:-5}"
settle_delay="${SETTLE_DELAY:-15}"
recreate_delay="${RECREATE_DELAY:-8}"
between_tries_delay="${BETWEEN_TRIES_DELAY:-8}"
internal_probe_timeout="${INTERNAL_PROBE_TIMEOUT:-45}"
cli_timeout="${CLI_TIMEOUT:-8}"
max_tries="${MAX_TRIES:-0}"
max_inconclusive="${MAX_INCONCLUSIVE:-0}"
daemon_binary="${DAEMON_BINARY:-$repo_root/almagestd}"
configured_cli_binary="${CLI_BINARY:-}"
skip_build="${SKIP_BUILD:-0}"

amnesia_connection_base64="${DEFAULT_TUN_FLAKE_AMNESIA_CONNECTION_BASE64:-}"
wireguard_a_private_key_base64="${DEFAULT_TUN_FLAKE_WIREGUARD_A_PRIVATE_KEY_BASE64:-}"
wireguard_tailscale_private_key_base64="${DEFAULT_TUN_FLAKE_WIREGUARD_TAILSCALE_PRIVATE_KEY_BASE64:-}"
amnesia_installation_uuid="${DEFAULT_TUN_FLAKE_AMNESIA_INSTALLATION_UUID:-}"
tailscale_mz_cache_base64="${DEFAULT_TUN_FLAKE_TAILSCALE_MZ_CACHE_BASE64:-}"
tailscale_cache_base64="${DEFAULT_TUN_FLAKE_TAILSCALE_CACHE_BASE64:-}"

# Each value is a regular expression for an operational label. Class-based
# rules tolerate generated node names and runtime counts, such as firewall
# rules installed by Tailscale. OPERATIONAL_NODES_JSON can add exact-name
# checks when a local graph needs them.
operational_classes_json="${OPERATIONAL_CLASSES_JSON:-{
  \"almagest/Amnesia\": \"^up$\",
  \"almagest/DefaultTun\": \"^ready$\",
  \"almagest/DnsCache\": \"^caching DNS$\",
  \"almagest/DnsClient\": \"^[1-9][0-9]* DNS servers?$\",
  \"almagest/DnsFallback\": \"^fallback across [1-9][0-9]* DNS backends$\",
  \"almagest/DnsQuery\": \"^ready$\",
  \"almagest/FetchAddr\": \"^(ready|success)$\",
  \"almagest/HttpClient\": \"^(ready|2[0-9][0-9] .+)$\",
  \"almagest/HttpServer\": \"^.+:[0-9]+$\",
  \"almagest/NetworkFirewall\": \"^filtering [0-9]+ rules$\",
  \"almagest/OutNetwork\": \"^out$\",
  \"almagest/TailscaleCtrl\": \"^up$\",
  \"almagest/Tun2Dns\": \"^adapting DNS$\",
  \"almagest/TunJoiner\": \"^joined default( \\\\+ [0-9]+ secondar(y|ies))?$\",
  \"almagest/TunSplitter\": \"^splitting$\",
  \"almagest/VTun\": \"^vtun$\",
  \"almagest/WireGuard\": \"^up$\"
}}"
operational_nodes_json="${OPERATIONAL_NODES_JSON:-{}}"

session_id="$(date -u +%Y%m%dT%H%M%SZ)-$$"
session_dir="$artifact_base/$session_id"
actions_log="$session_dir/actions.log"
summary_file="$session_dir/summary.txt"
cli_binary="$session_dir/bin/almagest-cli"
lock_file="$artifact_base/.capture.lock"

runtime_dir=""
runtime_base="/run"
config_dir=""
config_file=""
protected_config_file=""
file_storage=""
admin_socket=""
web_admin_port=""
web_admin_url=""

daemon_pid=""
daemon_launcher_pid=""
sudo_keepalive_pid=""
cleanup_active=0
try_dir=""

if ((EUID == 0)); then
	root_prefix=()
else
	root_prefix=(sudo -n)
fi

run_root() {
	"${root_prefix[@]}" "$@"
}

usage() {
	sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'
}

if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
	usage
	exit 0
fi
if (($# != 0)); then
	printf 'This script does not accept positional arguments. Use --help.\n' >&2
	exit 2
fi

timestamp() {
	date -u +'%Y-%m-%dT%H:%M:%S.%3NZ'
}

say() {
	local message="$*"
	printf '[%s] %s\n' "$(timestamp)" "$message" | tee -a "$actions_log"
}

action() {
	printf '[%s] %s\n' "$(timestamp)" "$*" >>"$actions_log"
}

die() {
	say "ERROR: $*"
	exit 1
}

require_command() {
	command -v "$1" >/dev/null 2>&1 || die "required command is not available: $1"
}

require_value() {
	local name="$1"
	local value="$2"
	[[ -n "$value" ]] || die "required environment variable is empty: $name"
}

require_base64() {
	local name="$1"
	local value="$2"
	require_value "$name" "$value"
	printf '%s' "$value" | base64 --decode >/dev/null 2>&1 \
		|| die "required environment variable is not valid base64: $name"
}

write_bootstrap_config() {
	run_root install -m 0600 /dev/stdin "$config_file" <<'EOF'
{
	"nodes": {
		"Amnesia VPN": {"Class": "pasta/StringConstant"},
		"WireGuard A": {"Class": "pasta/StringConstant"},
		"WireGuard Tailscale": {"Class": "pasta/StringConstant"}
	}
}
EOF
	run_root install -m 0600 /dev/stdin "$protected_config_file" <<'EOF'
{}
EOF
}

write_capture_config() {
	run_root install -m 0600 /dev/stdin "$config_file" <<'EOF'
{
	"nodes": {
		"OutNetwork r6": {"Class": "almagest/OutNetwork", "Pos": "{\"x\":866,\"y\":435}"},
		"VTun tQ": {
			"Class":          "almagest/VTun",
			"Pos":            "{\"x\":504,\"y\":457}",
			"Links":          ["Tun -> [WireGuard A] Tun"],
			"eventCh":        10,
			"noLoopbackAddr": false,
			"disableSACK":    false,
			"name":           "vtun",
			"localAddrs":     "",
			"dnsServers":     "",
			"mtu":            0,
			"endpointCh":     1024
		},
		"DnsQuery Et": {
			"Class": "almagest/DnsQuery",
			"Pos":   "{\"x\":-50,\"y\":561}",
			"Links": ["DNS -> [TailscaleCtrl mz] DNS"],
			"name":  "nixos-moth-thinkpad.",
			"type":  "A"
		},
		"HttpServer SO": {
			"Class":    "almagest/HttpServer",
			"Pos":      "{\"x\":35,\"y\":404}",
			"Links":    ["Network -> [NetworkFirewall kA] Network"],
			"host":     "100.97.143.122",
			"port":     80,
			"response": ""
		},
		"TailscaleCtrl mz": {
			"Class": "almagest/TailscaleCtrl",
			"Pos":   "{\"x\":128,\"y\":552}",
			"Links": [
				"WireGuard -> [WireGuard A] WGCtrl",
				"Network -> [OutNetwork r6] Network",
				"Firewall -> [NetworkFirewall kA] Config"
			],
			"hostname":         "",
			"authKeySecret":    "",
			"ephemeral":        false,
			"disableDERP":      false,
			"disableDiscovery": false,
			"storageID":        "b4969fd0d2e91e7d4490a38888b550c2e52865297048c43c950f69630b29e2f1",
			"enabled":          true,
			"controlURL":       "https://controlplane.tailscale.com",
			"listenPort":       0,
			"tlsConfig":        ""
		},
		"NetworkFirewall kA": {
			"Class": "almagest/NetworkFirewall",
			"Pos":   "{\"x\":312,\"y\":446}",
			"Links": ["Network -> [VTun tQ] Network"],
			"rules": {}
		},
		"OutNetwork": {"Class": "almagest/OutNetwork", "Pos": "{\"x\":993,\"y\":824}"},
		"WireGuard A": {
			"Class":            "almagest/WireGuard",
			"Pos":              "{\"x\":669,\"y\":504}",
			"Links":            ["Network -> [OutNetwork r6] Network"],
			"ownedIPs":         "",
			"dns":              "",
			"amneziaWGEnabled": false,
			"amneziaWG": {
				"cookieHeader":      "3",
				"cookiePadding":     0,
				"initHeader":        "1",
				"initPadding":       0,
				"initiationPackets": ["", "", "", "", ""],
				"junkCount":         0,
				"junkMax":           0,
				"junkMin":           0,
				"responseHeader":    "2",
				"responsePadding":   0,
				"transportHeader":   "4",
				"transportPadding":  0
			},
			"peers":            [],
			"privateKeySecret": "capture-wireguard-a-private-key",
			"listenPort":       0,
			"batchSize":        0
		},
		"WireGuard Tailscale": {
			"Class":            "almagest/WireGuard",
			"Pos":              "{\"x\":123,\"y\":880}",
			"Links":            ["Network -> [VTun ma] Network"],
			"peers":            [],
			"privateKeySecret": "capture-wireguard-tailscale-private-key",
			"listenPort":       0,
			"batchSize":        0,
			"ownedIPs":         "",
			"dns":              "",
			"amneziaWGEnabled": false,
			"amneziaWG": {
				"cookieHeader":      "3",
				"cookiePadding":     0,
				"initHeader":        "1",
				"initPadding":       0,
				"initiationPackets": ["", "", "", "", ""],
				"junkCount":         0,
				"junkMax":           0,
				"junkMin":           0,
				"responseHeader":    "2",
				"responsePadding":   0,
				"transportHeader":   "4",
				"transportPadding":  0
			}
		},
		"Amnesia VPN": {
			"Class":                  "almagest/Amnesia",
			"Pos":                    "{\"x\":797,\"y\":820}",
			"Links":                  ["Network -> [OutNetwork] Network"],
			"tlsConfig":              "default",
			"gatewayURL":             "",
			"gatewayPublicKeySecret": "",
			"storageID":              "2f8080c876dc985f87f6b44969dda0cf2878276f7ce8d26b9cfd65d5f0474b69",
			"enabled":                true,
			"connectionSecret":       "capture-amnesia-connection",
			"serverCountryCode":      "ae"
		},
		"TunSplitter": {
			"Class":                "almagest/TunSplitter",
			"Pos":                  "{\"x\":-76,\"y\":770}",
			"Links":                ["slot 1 -> [TunJoiner] Default", "slot 2 -> [WireGuard Tailscale] Tun"],
			"dnsCacheStorageName":  "",
			"rules":                "SNET4 100.64.0.0/10\nSNET6 fd7a:115c:a1e0::/48\nOR\nSLOT 2\n\nTRUE\nSLOT 1",
			"ruleCacheTTL":         0,
			"ruleCacheMaxEntries":  0,
			"routeCacheTTL":        0,
			"routeCacheMaxEntries": 0
		},
		"TailscaleCtrl": {
			"Class":            "almagest/TailscaleCtrl",
			"Pos":              "{\"x\":-51,\"y\":980}",
			"Links":            ["WireGuard -> [WireGuard Tailscale] WGCtrl", "Network -> [VTun ma] Network"],
			"enabled":          true,
			"hostname":         "",
			"authKeySecret":    "",
			"disableDERP":      false,
			"disableDiscovery": false,
			"tlsConfig":        "default",
			"storageID":        "47ba5b37b0e06f02d47c8a29ab9421acd115bc692d0ccd9de5fb8415a09372fb",
			"controlURL":       "https://controlplane.tailscale.com",
			"ephemeral":        false,
			"listenPort":       0
		},
		"HttpClient": {
			"Class":     "almagest/HttpClient",
			"Pos":       "{\"x\":-849,\"y\":700}",
			"Links":     ["Network -> [VTun Bu] Network"],
			"body":      "",
			"tlsConfig": "",
			"url":       "http://100.97.143.122",
			"method":    "GET"
		},
		"FetchAddr": {
			"Class":     "almagest/FetchAddr",
			"Pos":       "{\"x\":-857,\"y\":438}",
			"Links":     ["Network -> [VTun Bu] Network"],
			"provider":  "ifconfig.co",
			"tlsConfig": ""
		},
		"VTun Bu": {
			"Class":          "almagest/VTun",
			"Pos":            "{\"x\":-583,\"y\":713}",
			"Links":          ["Tun -> [TunJoiner vh] Secondary"],
			"name":           "vtun",
			"localAddrs":     "",
			"dnsServers":     "",
			"mtu":            0,
			"endpointCh":     1024,
			"eventCh":        10,
			"noLoopbackAddr": false,
			"disableSACK":    false
		},
		"VTun ma": {
			"Class":          "almagest/VTun",
			"Pos":            "{\"x\":326,\"y\":998}",
			"Links":          ["Tun -> [TunJoiner] Secondary"],
			"dnsServers":     "",
			"mtu":            0,
			"endpointCh":     1024,
			"eventCh":        10,
			"noLoopbackAddr": false,
			"disableSACK":    false,
			"name":           "vtun",
			"localAddrs":     ""
		},
		"TunJoiner": {
			"Class": "almagest/TunJoiner",
			"Pos":   "{\"x\":517,\"y\":812}",
			"Links": ["Tun -> [Amnesia VPN] Tun"]
		},
		"DnsClient Sg": {
			"Class":     "almagest/DnsClient",
			"Pos":       "{\"x\":91,\"y\":1136}",
			"Links":     ["Network -> [VTun ma] Network", "Bootstrap DNS -> [Amnesia VPN] DNS"],
			"tlsConfig": "default",
			"servers":   ["dot://dns.google:853"]
		},
		"DnsFallback tD": {
			"Class": "almagest/DnsFallback",
			"Pos":   "{\"x\":-324,\"y\":1099}",
			"Links": ["slot 1 -> [TailscaleCtrl] DNS", "slot 2 -> [DnsClient Sg] DNS"]
		},
		"DnsQuery XV": {
			"Class": "almagest/DnsQuery",
			"Pos":   "{\"x\":-790,\"y\":1289}",
			"Links": ["DNS -> [DnsCache vt] DNS"],
			"name":  "example.com.",
			"type":  "A"
		},
		"DnsQuery td": {
			"Class": "almagest/DnsQuery",
			"Pos":   "{\"x\":-790,\"y\":1160}",
			"Links": ["DNS -> [DnsCache vt] DNS"],
			"name":  "nixos-moth-thinkpad.",
			"type":  "A"
		},
		"DnsCache vt": {
			"Class":          "almagest/DnsCache",
			"Pos":            "{\"x\":-509,\"y\":1106}",
			"Links":          ["DNS -> [DnsFallback tD] DNS"],
			"storageName":    "default",
			"reverseLookups": false,
			"writeOnly":      false
		},
		"TunJoiner vh": {
			"Class": "almagest/TunJoiner",
			"Pos":   "{\"x\":-378,\"y\":772}",
			"Links": ["Tun -> [TunSplitter] Tun"]
		},
		"Tun2Dns Uw": {
			"Class": "almagest/Tun2Dns",
			"Pos":   "{\"x\":-671,\"y\":1014}",
			"Links": ["DNS -> [DnsCache vt] DNS"]
		},
		"TunSplitter lH": {
			"Class":                "almagest/TunSplitter",
			"Pos":                  "{\"x\":-856,\"y\":866}",
			"Links":                ["slot 1 -> [TunJoiner vh] Default", "slot 2 -> [Tun2Dns Uw] Tun"],
			"rules":                "PORT 53\nSLOT 2\n\nTRUE\nSLOT 1",
			"ruleCacheTTL":         0,
			"ruleCacheMaxEntries":  0,
			"routeCacheTTL":        0,
			"routeCacheMaxEntries": 0,
			"dnsCacheStorageName":  ""
		},
		"HttpClient A": {
			"Class":     "almagest/HttpClient",
			"Pos":       "{\"x\":770,\"y\":1023}",
			"Links":     ["Network -> [OutNetwork] Network"],
			"method":    "GET",
			"body":      "",
			"tlsConfig": "",
			"url":       "http://example.com"
		},
		"HttpClient B": {
			"Class":     "almagest/HttpClient",
			"Pos":       "{\"x\":106,\"y\":1268}",
			"Links":     ["Network -> [VTun ma] Network"],
			"body":      "",
			"tlsConfig": "",
			"url":       "http://example.com",
			"method":    "GET"
		},
		"DefaultTun c3": {
			"Class":     "almagest/DefaultTun",
			"Pos":       "{\"x\":-1079,\"y\":927}",
			"Links":     ["Tun -> [TunSplitter lH] Tun", "DNS -> [DnsCache vt] DNS"],
			"tunAddrs":  "",
			"tunRoutes": "",
			"mtu":       0,
			"dnsIP":     "",
			"strict":    false,
			"exclude":   [],
			"include":   []
		}
	}
}
EOF
}

# Append one command, its complete output, and its exit status to a file. A
# diagnostic command is allowed to be unavailable or fail.
capture_command() {
	local output="$1"
	shift
	{
		printf '\n[%s] $' "$(timestamp)"
		printf ' %q' "$@"
		printf '\n'
		local status
		if "$@"; then
			status=0
		else
			status=$?
		fi
		printf '[exit=%d]\n' "$status"
	} >>"$output" 2>&1
	return 0
}

# Write raw stdout to output and keep command, stderr, and status in sidecar
# files. Use this for JSON and other files that tools must be able to parse.
capture_output() {
	local output="$1"
	shift
	local status
	if "$@" >"$output" 2>"$output.stderr"; then
		status=0
	else
		status=$?
	fi
	{
		printf 'at=%s\ncommand=' "$(timestamp)"
		printf ' %q' "$@"
		printf '\nexit=%d\n' "$status"
	} >"$output.meta"
	return 0
}

daemon_is_alive() {
	[[ -n "$daemon_pid" ]] && run_root kill -0 "$daemon_pid" 2>/dev/null
}

cli() {
	run_root timeout --kill-after=2 "$cli_timeout" \
		"$cli_binary" --url "unix://$admin_socket" "$@"
}

stop_daemon() {
	if [[ -z "$daemon_pid" ]]; then
		return 0
	fi
	action "stopping daemon pid=$daemon_pid"
	if daemon_is_alive; then
		run_root kill -TERM "$daemon_pid" 2>/dev/null || true
		local deadline=$((SECONDS + 20))
		while daemon_is_alive && ((SECONDS <= deadline)); do
			sleep 0.25
		done
		if daemon_is_alive; then
			action "daemon did not stop after SIGTERM; sending SIGKILL"
			run_root kill -KILL "$daemon_pid" 2>/dev/null || true
		fi
	fi
	if [[ -n "$daemon_launcher_pid" ]]; then
		wait "$daemon_launcher_pid" 2>/dev/null || true
	fi
	daemon_pid=""
	daemon_launcher_pid=""
}

# shellcheck disable=SC2329 # Invoked through the EXIT, INT, and TERM traps.
cleanup() {
	local status=$?
	if ((cleanup_active != 0)); then
		exit "$status"
	fi
	cleanup_active=1
	trap - EXIT INT TERM
	stop_daemon || true
	if [[ -n "$runtime_dir" ]]; then
		case "$runtime_dir" in
			"$runtime_base"/almagest-default-tun-flake.*)
				run_root rm -rf -- "$runtime_dir" || true
				;;
			*)
				printf '[%s] Refusing to remove unexpected runtime path: %s\n' \
					"$(timestamp)" "$runtime_dir" >&2
				;;
			esac
	fi
	if [[ -n "$sudo_keepalive_pid" ]]; then
		kill "$sudo_keepalive_pid" 2>/dev/null || true
		wait "$sudo_keepalive_pid" 2>/dev/null || true
	fi
	if ((status != 0)); then
		printf '[%s] Capture stopped with status %d. Artifacts: %s\n' \
			"$(timestamp)" "$status" "$session_dir" | tee -a "$actions_log" >&2
	fi
	exit "$status"
}

trap cleanup EXIT
trap 'exit 130' INT TERM

mkdir -p "$session_dir/bin"
: >"$actions_log"
: >"$summary_file"
exec 9>"$lock_file"
flock -n 9 || die "another capture script owns $lock_file"

for command in base64 curl flock grep ip jq mktemp pgrep shuf ss timeout; do
	require_command "$command"
done
if ((EUID != 0)); then
	require_command sudo
fi
if [[ "$skip_build" == "0" ]]; then
	require_command go
	require_command just
elif [[ "$skip_build" != "1" ]]; then
	die "SKIP_BUILD must be 0 or 1"
fi
require_base64 DEFAULT_TUN_FLAKE_AMNESIA_CONNECTION_BASE64 "$amnesia_connection_base64"
require_base64 DEFAULT_TUN_FLAKE_WIREGUARD_A_PRIVATE_KEY_BASE64 "$wireguard_a_private_key_base64"
require_base64 DEFAULT_TUN_FLAKE_WIREGUARD_TAILSCALE_PRIVATE_KEY_BASE64 "$wireguard_tailscale_private_key_base64"
require_value DEFAULT_TUN_FLAKE_AMNESIA_INSTALLATION_UUID "$amnesia_installation_uuid"
[[ "$amnesia_installation_uuid" =~ ^[[:xdigit:]]{8}-[[:xdigit:]]{4}-4[[:xdigit:]]{3}-[89abAB][[:xdigit:]]{3}-[[:xdigit:]]{12}$ ]] \
	|| die "DEFAULT_TUN_FLAKE_AMNESIA_INSTALLATION_UUID is not a version 4 UUID"
require_base64 DEFAULT_TUN_FLAKE_TAILSCALE_MZ_CACHE_BASE64 "$tailscale_mz_cache_base64"
require_base64 DEFAULT_TUN_FLAKE_TAILSCALE_CACHE_BASE64 "$tailscale_cache_base64"
jq -e . <<<"$operational_classes_json" >/dev/null \
	|| die "OPERATIONAL_CLASSES_JSON is not valid JSON"
jq -e . <<<"$operational_nodes_json" >/dev/null \
	|| die "OPERATIONAL_NODES_JSON is not valid JSON"
[[ "$host_failure_confirmations" =~ ^[1-9][0-9]*$ ]] \
	|| die "HOST_FAILURE_CONFIRMATIONS must be a positive integer"

say "Preparing capture session $session_id"
if ((EUID != 0)); then
	action "requesting sudo credentials"
	sudo -v
	# Keep the timestamp valid during an unlimited series of tries.
	(
		while sudo -n true 2>/dev/null; do
			sleep 45
		done
	) &
	sudo_keepalive_pid="$!"
fi

if run_root pgrep -x almagestd >/dev/null 2>&1; then
	die "an almagestd process is already running"
fi

runtime_dir="$(run_root mktemp -d "$runtime_base/almagest-default-tun-flake.XXXXXXXXXX")"
config_dir="$runtime_dir/config"
config_file="$config_dir/alm.hujson"
protected_config_file="$config_dir/protected_alm.hujson"
file_storage="$runtime_dir/store"
admin_socket="$runtime_dir/admin.sock"
run_root install -d -m 0700 "$config_dir" "$file_storage"
write_bootstrap_config

for _ in {1..100}; do
	web_admin_port="$(shuf -i 20000-60000 -n 1)"
	if ! ss -H -ltn "sport = :$web_admin_port" | grep -q .; then
		break
	fi
	web_admin_port=""
done
[[ -n "$web_admin_port" ]] || die "failed to select an unused web admin port"
web_admin_url="http://127.0.0.1:$web_admin_port"

{
	printf 'session_id=%s\n' "$session_id"
	printf 'started_at=%s\n' "$(timestamp)"
	printf 'repo_root=%s\n' "$repo_root"
	printf 'host_probe_url=%s\n' "$host_probe_url"
	printf 'host_failure_confirmations=%s\n' "$host_failure_confirmations"
	printf 'host_failure_confirm_delay=%s\n' "$host_failure_confirm_delay"
	printf 'admin_socket=%s\n' "$admin_socket"
	printf 'config_dir=%s\n' "$config_dir"
	printf 'file_storage=%s\n' "$file_storage"
	printf 'web_admin_url=%s\n' "$web_admin_url"
	printf 'operational_classes=%s\n' "$operational_classes_json"
	printf 'operational_nodes=%s\n' "$operational_nodes_json"
} >>"$summary_file"
capture_command "$session_dir/environment.txt" uname -a
capture_command "$session_dir/environment.txt" id
capture_command "$session_dir/environment.txt" git status --short
capture_command "$session_dir/environment.txt" git rev-parse HEAD
capture_command "$session_dir/environment.txt" go version

if [[ "$skip_build" == "0" ]]; then
	say "Building daemon and capture CLI"
	if ! just build >"$session_dir/build.log" 2>&1; then
		die "just build failed; see $session_dir/build.log"
	fi
	if ! go build -o "$cli_binary" ./bin/almagest-cli >>"$session_dir/build.log" 2>&1; then
		die "CLI build failed; see $session_dir/build.log"
	fi
else
	say "Using prebuilt daemon and capture CLI"
	[[ -x "$daemon_binary" ]] || die "daemon executable is not available: $daemon_binary"
	[[ -n "$configured_cli_binary" && -x "$configured_cli_binary" ]] \
		|| die "CLI_BINARY must identify an executable when SKIP_BUILD=1"
	cli_binary="$configured_cli_binary"
fi

network_snapshot() {
	local output_dir="$1"
	mkdir -p "$output_dir"
	action "capturing system networking in $output_dir"
	local output="$output_dir/network.txt"
	: >"$output"
	capture_command "$output" date --iso-8601=ns
	capture_command "$output" ip -details -statistics link show
	capture_command "$output" ip -details address show
	capture_command "$output" ip -4 route show table all
	capture_command "$output" ip -6 route show table all
	capture_command "$output" ip -4 rule show
	capture_command "$output" ip -6 rule show
	capture_command "$output" ip neigh show
	capture_command "$output" ip -4 route get 1.1.1.1
	capture_command "$output" ip -6 route get 2606:4700:4700::1111
	capture_command "$output" ss -s
	capture_command "$output" run_root ss -plunt
	capture_command "$output" readlink -f /etc/resolv.conf
	capture_command "$output" sed -n 1,240p /etc/resolv.conf
	capture_command "$output" getent ahosts example.com
	capture_command "$output" resolvectl status
	capture_command "$output" resolvectl dns
	capture_command "$output" resolvectl domain
	capture_command "$output" networkctl status --all --no-pager
	capture_command "$output" nmcli general status
	capture_command "$output" nmcli device status
	capture_command "$output" nmcli connection show --active
	capture_command "$output" run_root nft list ruleset
	capture_command "$output" run_root iptables-save
	capture_command "$output" run_root ip6tables-save
	capture_command "$output" sysctl \
		net.ipv4.ip_forward \
		net.ipv4.conf.all.rp_filter \
		net.ipv4.conf.default.rp_filter \
		net.ipv4.conf.all.src_valid_mark \
		net.ipv6.conf.all.forwarding
	capture_command "$output" run_root conntrack -S
}

capture_runtime_snapshot() {
	local output_dir="$1"
	mkdir -p "$output_dir"
	action "capturing daemon and graph state in $output_dir"
	if daemon_is_alive; then
		capture_command "$output_dir/admin.txt" cli admin ping --nonce "capture-$session_id"
		capture_output "$output_dir/graph-map.json" cli graph map
		capture_output "$output_dir/workspace-snapshot.json" cli engine workspace-snapshot
		capture_output "$output_dir/workers-snapshot.json" cli workers snapshot
		capture_output "$output_dir/workers-tree.txt" cli workers tree
		capture_output "$output_dir/log-cache.json" cli log subscribe
		local name safe_name default_tun_name
		default_tun_name="$(node_record_by_class almagest/DefaultTun 2>/dev/null \
			| jq -r '.name // empty' 2>/dev/null || true)"
		for name in \
			"$default_tun_name" "TunSplitter lH" "TunJoiner vh" TunSplitter TunJoiner \
			"Amnesia VPN" TailscaleCtrl "TailscaleCtrl mz" \
			FetchAddr HttpClient "HttpClient A" "HttpClient B"; do
			[[ -n "$name" ]] || continue
			safe_name="${name// /_}"
			capture_output "$output_dir/menu-$safe_name.json" cli graph menu --name "$name"
		done
		capture_output "$output_dir/daemon-threads.txt" run_root ps -L -o pid,tid,stat,wchan:32,comm -p "$daemon_pid"
		capture_output "$output_dir/daemon-fds.txt" run_root ls -l "/proc/$daemon_pid/fd"
		capture_output "$output_dir/goroutines.txt" curl --noproxy '*' --fail --silent --show-error \
			"$web_admin_url/debug/pprof/goroutine?debug=2"
	fi
	network_snapshot "$output_dir/system"
}

host_probe() {
	local output_dir="$1"
	local include_diagnostics="${2:-1}"
	mkdir -p "$output_dir"
	action "probing host connectivity with $host_probe_url"
	if [[ "$include_diagnostics" == "1" ]]; then
		local diagnostics="$output_dir/diagnostics.txt"
		: >"$diagnostics"
		capture_command "$diagnostics" date --iso-8601=ns
		capture_command "$diagnostics" getent ahosts "$(sed -E 's#^[a-zA-Z]+://([^/:]+).*#\1#' <<<"$host_probe_url")"
		capture_command "$diagnostics" resolvectl query "$(sed -E 's#^[a-zA-Z]+://([^/:]+).*#\1#' <<<"$host_probe_url")"
		capture_command "$diagnostics" ip -4 route get 1.1.1.1
		capture_command "$diagnostics" ping -n -c 3 -W 3 1.1.1.1
		capture_command "$diagnostics" tracepath -n 1.1.1.1
	fi

	local status
	if curl --noproxy '*' --verbose --fail --show-error --location \
		--connect-timeout 10 --max-time 30 \
		--output "$output_dir/curl-body.txt" \
		--write-out $'remote_ip=%{remote_ip}\nlocal_ip=%{local_ip}\nhttp_code=%{http_code}\ntime_total=%{time_total}\n' \
		"$host_probe_url" >"$output_dir/curl-result.txt" 2>"$output_dir/curl-verbose.txt"; then
		status=0
	else
		status=$?
	fi
	printf 'curl_exit=%d\nurl=%s\nat=%s\n' "$status" "$host_probe_url" "$(timestamp)" \
		>"$output_dir/status.txt"
	return "$status"
}

# Return 0 only when the host failure is sustained and fresh in-daemon probes
# still pass. Return 1 when a later host probe succeeds, and 2 when a graph or
# in-daemon control stops being healthy.
confirm_host_failure() {
	local output_dir="$1"
	mkdir -p "$output_dir"
	local confirmation
	for ((confirmation = 2; confirmation <= host_failure_confirmations; confirmation++)); do
		sleep "$host_failure_confirm_delay"
		if ! workspace_operational_state "$output_dir/operational-$confirmation.json"; then
			return 2
		fi
		if host_probe "$output_dir/host-$confirmation" 0; then
			return 1
		fi
	done
	if ! run_internal_probes "$output_dir/internal-controls"; then
		return 2
	fi
	return 0
}

workspace_operational_state() {
	local output="$1"
	local raw="$output.raw"
	if ! cli engine workspace-snapshot >"$raw" 2>"$output.stderr"; then
		return 1
	fi
	if ! jq \
		--argjson expectedClasses "$operational_classes_json" \
		--argjson expectedNodes "$operational_nodes_json" '
		.snapshot.nodes
		| [
			to_entries[]
			| {
				id: .key,
				name: .value.name,
				class: .value.class,
				label: (.value.label // "")
			}
		] as $nodes
		| [
			$expectedClasses | to_entries[] as $rule
			| ($nodes | map(select(.class == $rule.key))) as $matching
			| if ($matching | length) == 0 then
				{
					class: $rule.key,
					expected: $rule.value,
					actual: null,
					problem: "class is absent"
				}
			  else
				($matching[]
					| select((.label | test($rule.value)) | not)
					| {
						name: .name,
						class: .class,
						expected: $rule.value,
						actual: .label
					})
			  end
		] as $classMismatches
		| [
			$expectedNodes | to_entries[] as $rule
			| ($nodes | map(select(.name == $rule.key)) | first) as $node
			| select(($node == null) or (($node.label | test($rule.value)) | not))
			| {name: $rule.key, expected: $rule.value, actual: ($node.label // null)}
		] as $nodeMismatches
		| [
			$nodes[]
			| select($expectedClasses[.class] == null)
			| select(.label | test("^(waiting|starting|idle|error|fail|invalid|listen failed|TUN error|network detached)"; "i"))
			| {
				name: .name,
				class: .class,
				expected: "operational label",
				actual: .label
			}
		] as $unknownMismatches
		| (($classMismatches + $nodeMismatches + $unknownMismatches)
			| unique_by(.name, .class)) as $mismatches
		| {
			operational: (($mismatches | length) == 0),
			expected_classes: $expectedClasses,
			expected_nodes: $expectedNodes,
			actual: ($nodes
				| map({
					key: .name,
					value: {class: .class, label: .label}
				})
				| from_entries),
			mismatches: $mismatches
		}
	' "$raw" >"$output" 2>>"$output.stderr"; then
		return 1
	fi
	jq -e '.operational' "$output" >/dev/null
}

wait_for_operational_nodes() {
	local output_dir="$1"
	mkdir -p "$output_dir"
	action "waiting for all required nodes to stay operational for ${operational_stable_seconds}s"
	local deadline=$((SECONDS + operational_timeout))
	local stable_since=0
	while ((SECONDS <= deadline)); do
		if ! daemon_is_alive; then
			action "daemon exited while waiting for operational nodes"
			return 1
		fi
		if workspace_operational_state "$output_dir/latest.json"; then
			if ((stable_since == 0)); then
				stable_since=$SECONDS
			fi
			if ((SECONDS - stable_since >= operational_stable_seconds)); then
				cp "$output_dir/latest.json" "$output_dir/operational.json"
				action "all required nodes are operational"
				return 0
			fi
		else
			stable_since=0
		fi
		sleep 1
	done
	cp "$output_dir/latest.json" "$output_dir/timeout.json" 2>/dev/null || true
	action "timed out waiting for operational nodes"
	return 1
}

menu_result_value() {
	local menu="$1"
	local field="$2"
	jq -r --arg field "$field" '
		.blocks[] | select(.id == "result")
		| .items[] | select(.id == $field)
		| (.text // .value // .field.value // "")
	' "$menu"
}

wait_internal_probe() {
	local name="$1"
	local kind="$2"
	local output="$3"
	local deadline=$((SECONDS + internal_probe_timeout))
	local temp="$output.tmp"
	while ((SECONDS <= deadline)); do
		if ! cli graph menu --name "$name" >"$temp" 2>"$output.stderr"; then
			sleep 0.5
			continue
		fi
		local status error_text response
		status="$(menu_result_value "$temp" status 2>/dev/null || true)"
		error_text="$(menu_result_value "$temp" error 2>/dev/null || true)"
		if [[ "$kind" == "fetch" ]]; then
			response="$(menu_result_value "$temp" publicIP 2>/dev/null || true)"
			if [[ "$status" == "Status: success" && -z "$error_text" && -n "$response" ]]; then
				mv "$temp" "$output"
				return 0
			fi
			if [[ "$status" != "Status: checking..." ]]; then
				mv "$temp" "$output"
				return 1
			fi
		else
			response="$(menu_result_value "$temp" response 2>/dev/null || true)"
			if [[ "$status" =~ ^Status:\ 2[0-9][0-9] && -z "$error_text" ]]; then
				mv "$temp" "$output"
				return 0
			fi
			if [[ "$status" != "Status: running..." ]]; then
				mv "$temp" "$output"
				return 1
			fi
		fi
		sleep 0.5
	done
	mv "$temp" "$output" 2>/dev/null || true
	return 1
}

run_internal_probes() {
	local output_dir="$1"
	mkdir -p "$output_dir"
	action "starting fresh in-daemon probes"
	local click_failed=0
	if ! cli graph click-menu-button --name FetchAddr --block check --button check \
		>"$output_dir/click-FetchAddr.json" 2>"$output_dir/click-FetchAddr.stderr"; then
		click_failed=1
	fi
	local name safe_name
	for name in HttpClient "HttpClient A" "HttpClient B"; do
		safe_name="${name// /_}"
		if ! cli graph click-menu-button --name "$name" --block request --button send \
			>"$output_dir/click-$safe_name.json" 2>"$output_dir/click-$safe_name.stderr"; then
			click_failed=1
		fi
	done

	local failed=$click_failed
	local result
	if wait_internal_probe FetchAddr fetch "$output_dir/result-FetchAddr.json"; then
		result=success
	else
		result=fail
		failed=1
	fi
	printf 'FetchAddr\t%s\n' "$result" >>"$output_dir/summary.tsv"
	for name in HttpClient "HttpClient A" "HttpClient B"; do
		safe_name="${name// /_}"
		if wait_internal_probe "$name" http "$output_dir/result-$safe_name.json"; then
			result=success
		else
			result=fail
			failed=1
		fi
		printf '%s\t%s\n' "$name" "$result" >>"$output_dir/summary.tsv"
	done
	if ((failed != 0)); then
		action "one or more in-daemon probes failed"
		return 1
	fi
	action "all in-daemon probes succeeded"
	return 0
}

node_id_by_name() {
	local name="$1"
	cli graph map | jq -er --arg name "$name" '.nodes[$name].ID'
}

node_record_by_class() {
	local class="$1"
	cli graph map | jq -er --arg class "$class" '
		[.nodes | to_entries[]
			| select(.value.Class == $class)
			| {name: .key, id: .value.ID}]
		| if length == 1 then .[0]
		  else error("expected exactly one node of class " + $class)
		  end
	'
}

node_port_id() {
	local node="$1"
	local direction="$2"
	local name="$3"
	cli engine workspace-snapshot | jq -er \
		--arg node "$node" --arg direction "$direction" --arg name "$name" '
		.snapshot as $workspace
		| $workspace.nodes[$node] as $nodeSnapshot
		| ($nodeSnapshot[$direction + "_ports"] // [])[]
		| . as $portID
		| $workspace.ports[$portID | tostring]
		| select(.name == $name)
		| $portID
	'
}

add_link_by_port_name() {
	local node_a="$1" direction_a="$2" name_a="$3"
	local node_b="$4" direction_b="$5" name_b="$6"
	local port_a port_b
	port_a="$(node_port_id "$node_a" "$direction_a" "$name_a")"
	port_b="$(node_port_id "$node_b" "$direction_b" "$name_b")"
	cli graph add-link --port-a "$port_a" --port-b "$port_b"
}

recreate_default_tun() {
	local output_dir="$1"
	mkdir -p "$output_dir"
	local old_record old_name old_id splitter_id dns_id new_id
	old_record="$(node_record_by_class almagest/DefaultTun)"
	old_name="$(jq -r '.name' <<<"$old_record")"
	old_id="$(jq -r '.id' <<<"$old_record")"
	splitter_id="$(node_id_by_name "TunSplitter lH")"
	dns_id="$(node_id_by_name "DnsCache vt")"
	printf 'old_default_tun_name=%s\nold_default_tun_id=%s\nsplitter_id=%s\ndns_cache_id=%s\n' \
		"$old_name" "$old_id" "$splitter_id" "$dns_id" >"$output_dir/ids.txt"

	say "Confirmed target failure. Deleting DefaultTun node $old_name ($old_id)"
	cli graph delete-node --id "$old_id" >"$output_dir/delete.json" 2>"$output_dir/delete.stderr"
	local deadline=$((SECONDS + 30))
	while node_id_by_name "$old_name" >/dev/null 2>&1; do
		((SECONDS <= deadline)) || return 1
		sleep 0.25
	done
	sleep "$recreate_delay"
	capture_runtime_snapshot "$output_dir/after-delete"

	say "Creating and reconnecting DefaultTun"
	new_id="$(cli engine add-node --class almagest/DefaultTun --name "$old_name" \
		| tee "$output_dir/add-node.json" | jq -er '.id')"
	printf 'new_default_tun_id=%s\n' "$new_id" >>"$output_dir/ids.txt"
	add_link_by_port_name "$new_id" right Tun "$splitter_id" left Tun \
		>"$output_dir/add-tun-link.json" 2>"$output_dir/add-tun-link.stderr"
	add_link_by_port_name "$new_id" right DNS "$dns_id" left DNS \
		>"$output_dir/add-dns-link.json" 2>"$output_dir/add-dns-link.stderr"
	sleep "$recreate_delay"
	return 0
}

start_daemon() {
	local output_dir="$1"
	local pid_file="$output_dir/daemon.pid"
	action "starting daemon with the daemon-sudo arguments"
	# The root shell writes its PID and then becomes almagestd. This gives the
	# script the real daemon PID without matching unrelated processes.
	# shellcheck disable=SC2016,SC2024 # The quoted root shell owns the log file.
	run_root bash -c '
		pid_file=$1
		shift
		printf "%s\n" "$$" >"$pid_file"
		exec "$@"
	' capture-almagestd "$pid_file" "$daemon_binary" \
		-c "$config_dir" --file-storage "$file_storage" --unix "$admin_socket" \
		-l debug -w "$web_admin_port" --web-pprof --debug-nodes --experimental \
		--unsafe-config --no-dns-cache-expiration >"$output_dir/daemon.log" 2>&1 &
	daemon_launcher_pid="$!"

	local deadline=$((SECONDS + 20))
	while [[ ! -s "$pid_file" ]]; do
		kill -0 "$daemon_launcher_pid" 2>/dev/null || return 1
		((SECONDS <= deadline)) || return 1
		sleep 0.1
	done
	daemon_pid="$(<"$pid_file")"
	deadline=$((SECONDS + 60))
	while ((SECONDS <= deadline)); do
		if cli admin ping --nonce "$session_id" >"$output_dir/admin-ping.json" 2>"$output_dir/admin-ping.stderr"; then
			action "daemon admin API is ready: pid=$daemon_pid"
			return 0
		fi
		daemon_is_alive || return 1
		sleep 0.5
	done
	return 1
}

start_seed_daemon() {
	local output_dir="$1"
	local pid_file="$output_dir/daemon.pid"
	action "starting the temporary storage-seed daemon"
	# shellcheck disable=SC2016,SC2024 # The quoted root shell owns the log file.
	run_root bash -c '
		pid_file=$1
		shift
		printf "%s\n" "$$" >"$pid_file"
		exec "$@"
	' capture-seed-almagestd "$pid_file" "$daemon_binary" \
		-c "$config_dir" --file-storage "$file_storage" --unix "$admin_socket" \
		-l error --debug-nodes --experimental --unsafe-config --no-dns-cache-expiration \
		>"$output_dir/daemon.log" 2>&1 &
	daemon_launcher_pid="$!"

	local deadline=$((SECONDS + 20))
	while [[ ! -s "$pid_file" ]]; do
		kill -0 "$daemon_launcher_pid" 2>/dev/null || return 1
		((SECONDS <= deadline)) || return 1
		sleep 0.1
	done
	daemon_pid="$(<"$pid_file")"
	deadline=$((SECONDS + 30))
	while ((SECONDS <= deadline)); do
		if cli admin ping --nonce "seed-$session_id" >/dev/null 2>"$output_dir/admin-ping.stderr"; then
			action "temporary storage-seed daemon is ready: pid=$daemon_pid"
			return 0
		fi
		daemon_is_alive || return 1
		sleep 0.25
	done
	return 1
}

seed_runtime_storage() {
	local output_dir="$session_dir/seed"
	mkdir -p "$output_dir"
	say "Populating the temporary KV and secrets storage"
	if ! start_seed_daemon "$output_dir"; then
		stop_daemon
		die "temporary storage-seed daemon did not start; see $output_dir/daemon.log"
	fi

	local amnesia_connection wireguard_a_private_key wireguard_tailscale_private_key
	amnesia_connection="$(printf '%s' "$amnesia_connection_base64" | base64 --decode)"
	wireguard_a_private_key="$(printf '%s' "$wireguard_a_private_key_base64" | base64 --decode)"
	wireguard_tailscale_private_key="$(printf '%s' "$wireguard_tailscale_private_key_base64" | base64 --decode)"

	cli secrets set --key capture-amnesia-connection --value "$amnesia_connection" \
		>/dev/null 2>"$output_dir/secret-amnesia.stderr" \
		|| die "failed to store the Amnesia credential"
	cli secrets set-nodes --key capture-amnesia-connection --node "Amnesia VPN" \
		>/dev/null 2>>"$output_dir/secret-amnesia.stderr" \
		|| die "failed to allow Amnesia VPN to use its credential"
	cli secrets set --key capture-wireguard-a-private-key --value "$wireguard_a_private_key" \
		>/dev/null 2>"$output_dir/secret-wireguard-a.stderr" \
		|| die "failed to store the WireGuard A private key"
	cli secrets set-nodes --key capture-wireguard-a-private-key --node "WireGuard A" \
		>/dev/null 2>>"$output_dir/secret-wireguard-a.stderr" \
		|| die "failed to allow WireGuard A to use its private key"
	cli secrets set --key capture-wireguard-tailscale-private-key \
		--value "$wireguard_tailscale_private_key" \
		>/dev/null 2>"$output_dir/secret-wireguard-tailscale.stderr" \
		|| die "failed to store the Tailscale WireGuard private key"
	cli secrets set-nodes --key capture-wireguard-tailscale-private-key \
		--node "WireGuard Tailscale" \
		>/dev/null 2>>"$output_dir/secret-wireguard-tailscale.stderr" \
		|| die "failed to allow WireGuard Tailscale to use its private key"

	local amnesia_path tailscale_mz_path tailscale_path
	amnesia_path="nodes/amnesia/2f8080c876dc985f87f6b44969dda0cf2878276f7ce8d26b9cfd65d5f0474b69"
	tailscale_mz_path="nodes/tailscale/b4969fd0d2e91e7d4490a38888b550c2e52865297048c43c950f69630b29e2f1"
	tailscale_path="nodes/tailscale/47ba5b37b0e06f02d47c8a29ab9421acd115bc692d0ccd9de5fb8415a09372fb"
	cli kv ensure --path "$amnesia_path" >/dev/null 2>"$output_dir/kv-amnesia.stderr" \
		|| die "failed to create the Amnesia KV subtree"
	cli kv set --path "$amnesia_path" --key installation_uuid \
		--data "$amnesia_installation_uuid" --hidden --string=true \
		>/dev/null 2>>"$output_dir/kv-amnesia.stderr" \
		|| die "failed to store the Amnesia installation UUID"
	cli kv ensure --path "$tailscale_mz_path" >/dev/null 2>"$output_dir/kv-tailscale-mz.stderr" \
		|| die "failed to create the TailscaleCtrl mz KV subtree"
	cli kv set --path "$tailscale_mz_path" --key client_cache \
		--data "$tailscale_mz_cache_base64" --encoding base64 --hidden --string=false \
		>/dev/null 2>>"$output_dir/kv-tailscale-mz.stderr" \
		|| die "failed to store the TailscaleCtrl mz client cache"
	cli kv ensure --path "$tailscale_path" >/dev/null 2>"$output_dir/kv-tailscale.stderr" \
		|| die "failed to create the TailscaleCtrl KV subtree"
	cli kv set --path "$tailscale_path" --key client_cache \
		--data "$tailscale_cache_base64" --encoding base64 --hidden --string=false \
		>/dev/null 2>>"$output_dir/kv-tailscale.stderr" \
		|| die "failed to store the TailscaleCtrl client cache"

	stop_daemon
	action "temporary KV and secrets storage is populated"
}

finish_try() {
	local label="$1"
	if [[ -n "$try_dir" ]]; then
		capture_runtime_snapshot "$try_dir/$label-before-stop"
	fi
	stop_daemon
	if [[ -n "$try_dir" ]]; then
		network_snapshot "$try_dir/$label-after-stop"
	fi
}

seed_runtime_storage

try_number=1
consecutive_inconclusive=0
while ((max_tries == 0 || try_number <= max_tries)); do
	try_dir="$session_dir/try-$(printf '%04d' "$try_number")"
	mkdir -p "$try_dir"
	write_capture_config
	say "Try $try_number: checking the baseline host network"
	network_snapshot "$try_dir/00-before-daemon"
	if ! host_probe "$try_dir/00-before-daemon/host-probe"; then
		die "the host probe failed before daemon start; see $try_dir/00-before-daemon"
	fi

	say "Try $try_number: starting daemon"
	if ! start_daemon "$try_dir"; then
		finish_try startup-failed
		die "daemon did not start; see $try_dir/daemon.log"
	fi
	sleep "$initial_delay"
	capture_runtime_snapshot "$try_dir/01-after-initial-delay"

	say "Try $try_number: waiting for all required nodes"
	if ! wait_for_operational_nodes "$try_dir/02-operational-wait"; then
		say "Try $try_number is inconclusive: nodes did not become operational"
		printf 'result=inconclusive_nodes\n' >"$try_dir/result.txt"
		finish_try inconclusive
		((consecutive_inconclusive += 1))
		if ((max_inconclusive > 0 && consecutive_inconclusive >= max_inconclusive)); then
			die "reached MAX_INCONCLUSIVE=$max_inconclusive"
		fi
		((try_number += 1))
		sleep "$between_tries_delay"
		continue
	fi
	sleep "$settle_delay"
	if ! workspace_operational_state "$try_dir/02-operational-after-settle.json"; then
		say "Try $try_number is inconclusive: a required node left its operational state during the settle delay"
		printf 'result=inconclusive_unstable_nodes\n' >"$try_dir/result.txt"
		finish_try inconclusive
		((consecutive_inconclusive += 1))
		if ((max_inconclusive > 0 && consecutive_inconclusive >= max_inconclusive)); then
			die "reached MAX_INCONCLUSIVE=$max_inconclusive"
		fi
		((try_number += 1))
		sleep "$between_tries_delay"
		continue
	fi
	capture_runtime_snapshot "$try_dir/02-operational"

	say "Try $try_number: running in-daemon probes"
	if ! run_internal_probes "$try_dir/03-internal-probes"; then
		say "Try $try_number is inconclusive: an in-daemon probe failed"
		printf 'result=inconclusive_internal_probe\n' >"$try_dir/result.txt"
		finish_try inconclusive
		((consecutive_inconclusive += 1))
		if ((max_inconclusive > 0 && consecutive_inconclusive >= max_inconclusive)); then
			die "reached MAX_INCONCLUSIVE=$max_inconclusive"
		fi
		((try_number += 1))
		sleep "$between_tries_delay"
		continue
	fi
	consecutive_inconclusive=0

	say "Try $try_number: running verbose host probe"
	if host_probe "$try_dir/04-host-probe"; then
		say "Try $try_number is clean; host networking works"
		printf 'result=clean\n' >"$try_dir/result.txt"
		finish_try clean
		((try_number += 1))
		sleep "$between_tries_delay"
		continue
	fi

	say "Try $try_number: first host probe failed; checking that the failure is sustained"
	capture_runtime_snapshot "$try_dir/05-first-host-failure"
	if confirm_host_failure "$try_dir/05-host-failure-confirmation"; then
		confirmation_status=0
	else
		confirmation_status=$?
	fi
	if ((confirmation_status != 0)); then
		if ((confirmation_status == 1)); then
			say "Try $try_number is inconclusive: the host probe recovered without graph changes"
			printf 'result=inconclusive_transient_host_probe\n' >"$try_dir/result.txt"
		else
			say "Try $try_number is inconclusive: a control failed during host failure confirmation"
			printf 'result=inconclusive_confirmation_control\n' >"$try_dir/result.txt"
		fi
		finish_try inconclusive
		((consecutive_inconclusive += 1))
		if ((max_inconclusive > 0 && consecutive_inconclusive >= max_inconclusive)); then
			die "reached MAX_INCONCLUSIVE=$max_inconclusive"
		fi
		((try_number += 1))
		sleep "$between_tries_delay"
		continue
	fi

	say "Try $try_number reproduced a sustained target failure"
	printf 'result=bug_reproduced\n' >"$try_dir/result.txt"
	capture_runtime_snapshot "$try_dir/05-broken"
	if ! recreate_default_tun "$try_dir/06-recreate"; then
		printf 'repair_result=recreate_failed\n' >>"$try_dir/result.txt"
		finish_try recreate-failed
		die "DefaultTun recreation failed; the target bug was captured"
	fi

	say "Waiting for the recreated graph to become operational"
	if ! wait_for_operational_nodes "$try_dir/07-recreated-operational-wait"; then
		printf 'repair_result=nodes_not_operational\n' >>"$try_dir/result.txt"
		finish_try repair-failed
		die "the recreated graph did not become operational; the target bug was captured"
	fi
	sleep "$settle_delay"
	capture_runtime_snapshot "$try_dir/07-recreated-operational"

	say "Repeating in-daemon and host probes after recreation"
	repair_failed=0
	if ! run_internal_probes "$try_dir/08-internal-probes-after-recreate"; then
		repair_failed=1
	fi
	if ! host_probe "$try_dir/09-host-probe-after-recreate"; then
		repair_failed=1
	fi
	capture_runtime_snapshot "$try_dir/10-final"
	if ((repair_failed != 0)); then
		printf 'repair_result=failed\n' >>"$try_dir/result.txt"
		finish_try repair-failed
		die "bug was captured, but recreation did not pass all checks"
	fi

	printf 'repair_result=success\n' >>"$try_dir/result.txt"
	{
		printf 'result=bug_reproduced_and_repaired\n'
		printf 'try=%d\n' "$try_number"
		printf 'completed_at=%s\n' "$(timestamp)"
		printf 'artifact=%s\n' "$try_dir"
	} >>"$summary_file"
	finish_try complete
	say "Capture complete. Bug reproduced and DefaultTun recreation restored connectivity."
	say "Artifacts: $session_dir"
	exit 0
done

printf 'result=max_tries_without_reproduction\ntries=%d\n' "$max_tries" >>"$summary_file"
say "No target failure was found in $max_tries tries. Artifacts: $session_dir"
exit 2
