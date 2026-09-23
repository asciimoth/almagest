#!/usr/bin/env bash
set -euo pipefail

command -v almagest-cli >/dev/null
command -v jq >/dev/null

rm -f \
	/tmp/almagest-e2e-time-trigger-interval.log \
	/tmp/almagest-e2e-time-trigger-ready.log
