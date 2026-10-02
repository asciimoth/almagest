#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
cd "$repo_root"

for architecture in amd64 arm64; do
    printf 'Linting Windows/%s packages\n' "$architecture"
    GOOS=windows GOARCH="$architecture" CGO_ENABLED=0 \
        golangci-lint run --build-tags=winintegration,almagest_e2e \
        ./certcat/... ./lib/... ./bin/...
done
