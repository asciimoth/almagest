#!/usr/bin/env bash
set -euo pipefail

rm -rf "${E2E_RUNTIME:?}/storage"
mkdir -p "${E2E_RUNTIME:?}/storage"
chmod 700 "${E2E_RUNTIME:?}/storage"
