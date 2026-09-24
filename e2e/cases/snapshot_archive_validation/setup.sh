#!/usr/bin/env bash
set -euo pipefail

for command in almagest-cli jq python3; do
	command -v "$command" >/dev/null
done

rm -rf /var/lib/almagest-e2e-snapshot-archive-validation
mkdir -p /var/lib/almagest-e2e-snapshot-archive-validation
chmod 700 /var/lib/almagest-e2e-snapshot-archive-validation

