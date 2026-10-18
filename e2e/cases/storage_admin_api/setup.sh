#!/usr/bin/env bash
set -euo pipefail

for command in almagest-cli jq; do
	command -v "$command" >/dev/null
done

rm -rf "${E2E_RUNTIME:?}/storage"
mkdir -p "${E2E_RUNTIME:?}/storage"
chmod 700 "${E2E_RUNTIME:?}/storage"
