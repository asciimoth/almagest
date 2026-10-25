#!/usr/bin/env bash
set -euo pipefail
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repo_root=$(cd -- "$script_dir/../.." && pwd -P)
version=${1:?usage: repro-package.sh VERSION}
[[ $version =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || {
    printf 'package version must be an MSI x.y.z version\n' >&2
    exit 2
}
artifact_root="$repo_root/artifacts/winvm"
mkdir -p "$artifact_root"
first=$(mktemp -d "$artifact_root/run-package-repro-a-XXXXXXXX")
second=$(mktemp -d "$artifact_root/run-package-repro-b-XXXXXXXX")
name="almagest_${version}_windows_amd64.msi"
ALMAGEST_PACKAGE_VERSION=$version WINVM_RUN_DIR=$first "$script_dir/run.sh" package
ALMAGEST_PACKAGE_VERSION=$version WINVM_RUN_DIR=$second "$script_dir/run.sh" package
first_hash=$(sha256sum "$first/package/$name" | awk '{print $1}')
second_hash=$(sha256sum "$second/package/$name" | awk '{print $1}')
equal=false
[[ $first_hash != "$second_hash" ]] || equal=true
jq -n --arg version "$version" --arg firstRun "$first" --arg secondRun "$second" \
    --arg firstSHA256 "$first_hash" --arg secondSHA256 "$second_hash" \
    --argjson byteForByte "$equal" \
    '{schemaVersion:1,version:$version,firstRun:$firstRun,secondRun:$secondRun,firstSHA256:$firstSHA256,secondSHA256:$secondSHA256,byteForByte:$byteForByte,outcome:(if $byteForByte then "passed" else "failed" end)}' \
    >"$second/package/reproducibility.json"
if [[ $equal != true ]]; then
    printf 'WinVM package builds differ. Evidence: %s\n' "$second/package/reproducibility.json" >&2
    exit 1
fi
printf 'Reproducible MSI: %s\nEvidence: %s\n' "$second/package/$name" "$second/package/reproducibility.json"
