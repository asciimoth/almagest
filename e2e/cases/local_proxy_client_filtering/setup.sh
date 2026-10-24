#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

command -v curl >/dev/null
command -v setpriv >/dev/null

ensure_e2e_user almproxyuser 24021 24021
cp "$(command -v curl)" /tmp/almproxy-denied
chmod 0755 /tmp/almproxy-denied
