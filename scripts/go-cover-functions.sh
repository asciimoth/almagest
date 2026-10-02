#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
profile="${1:?coverage profile required}"
output="${2:?functions output required}"
workspace_dir="$(mktemp -d "${TMPDIR:-/tmp}/almagest-cover-workspace.XXXXXX")"
trap 'rm -rf "$workspace_dir"' EXIT

GOWORK=off go -C "$workspace_dir" work init \
	"$repo_root/certcat" \
	"$repo_root/lib" \
	"$repo_root/bin"
GOWORK="$workspace_dir/go.work" go tool cover -func="$profile" >"$output"
