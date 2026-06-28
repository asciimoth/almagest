#!/usr/bin/env bash
set -euo pipefail

command -v jq >/dev/null
command -v ip >/dev/null

ip addr show lo >/dev/null
