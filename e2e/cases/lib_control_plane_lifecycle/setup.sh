#!/usr/bin/env bash
set -euo pipefail

for command in almagest-cli jq; do
	command -v "$command" >/dev/null
done

rm -rf /var/lib/almagest-e2e-lib-control-plane
mkdir -p /var/lib/almagest-e2e-lib-control-plane
chmod 700 /var/lib/almagest-e2e-lib-control-plane

