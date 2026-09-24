#!/usr/bin/env bash
set -euo pipefail

for command in almagest-cli jq; do
	command -v "$command" >/dev/null
done

