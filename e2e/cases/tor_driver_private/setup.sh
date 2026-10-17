#!/usr/bin/env bash
set -euo pipefail

command -v curl >/dev/null
command -v jq >/dev/null
command -v setpriv >/dev/null
command -v tor >/dev/null
test -x /opt/chutney/chutney
id tor-test >/dev/null
sed -i "s@%E2E_RUNTIME%@$E2E_RUNTIME@g" "$E2E_CONFIG_DIR/protected_alm.hujson"
