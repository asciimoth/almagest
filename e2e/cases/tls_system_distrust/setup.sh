#!/usr/bin/env bash
set -euo pipefail

command -v jq >/dev/null

cat >"$E2E_CONFIG_DIR/protected_alm.hujson" <<EOF
{
	"Admin": {"Web": 0, "Unix": "$E2E_ADMIN_SOCK"},
	"TLS": {"CRLs": {"Enabled": true}},
}
EOF
