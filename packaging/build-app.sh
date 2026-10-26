#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
output="$repo_root/build/packaging"
export_dir="$(mktemp -d)"
trap 'rm -rf -- "$export_dir"' EXIT

if [[ "$(go env GOHOSTOS)" != linux || "$(go env GOHOSTARCH)" != amd64 ]]; then
	printf 'desktop app packaging supports only a linux/amd64 build host\n' >&2
	exit 1
fi

docker build --platform linux/amd64 \
	--file "$repo_root/packaging/app/Dockerfile" \
	--output "type=local,dest=$export_dir" \
	"$repo_root"
install -Dm0755 "$export_dir/almagest-app" "$output/almagest-app"
