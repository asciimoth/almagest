#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

start_daemon
python3 ./subscription_trees.py "$E2E_ADMIN_SOCK"

log "case passed"

