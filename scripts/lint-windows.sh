#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
for architecture in amd64 arm64; do
    for module in certcat lib bin; do
        printf 'Linting %s packages for Windows/%s\n' "$module" "$architecture"
        (
            cd "$repo_root/$module"
            GOOS=windows GOARCH="$architecture" CGO_ENABLED=0 \
                golangci-lint run --build-tags=winintegration,almagest_e2e ./...
        )
    done
done
