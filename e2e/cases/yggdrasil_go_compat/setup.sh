#!/usr/bin/env bash
set -euo pipefail
command -v yggdrasil >/dev/null
command -v yggdrasilctl >/dev/null
command -v jq >/dev/null
[[ -c /dev/net/tun ]]
