#!/usr/bin/env bash
set -euo pipefail

command -v jq >/dev/null
command -v ip >/dev/null
command -v tcpdump >/dev/null
command -v tshark >/dev/null

ip addr show lo >/dev/null
