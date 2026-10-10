#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/../.." && pwd)"
output_dir="${1:-$repo_root/artifacts/linux-dns-matrix/$(date -u +%Y%m%dT%H%M%SZ)-$$}"
mkdir -p "$output_dir"
output_dir="$(cd -- "$output_dir" && pwd)"
work="$(mktemp -d)"
cases=(
    direct
    direct-no-upstream
    debian-resolvconf
    debian-resolvconf-no-upstream
    openresolv
    openresolv-no-upstream
    systemd-resolved
    systemd-resolved-no-upstream
    systemd-resolved-split
    systemd-resolved-dynamic-ifidx
)
cleanup() {
    for case_name in "${cases[@]}"; do
        docker image rm --force "sysnet-linux-dns-e2e:$case_name" >/dev/null 2>&1 || true
    done
    chmod -R u+w "$work" 2>/dev/null || true
    rm -rf -- "$work"
}
trap cleanup EXIT

sysnet_dir="$(go -C "$repo_root/bin" list -m -f '{{.Dir}}' github.com/asciimoth/sysnet-linux)"
cp -a -- "$sysnet_dir/." "$work/"
chmod -R u+w "$work"
chmod +x "$work/e2e/dns/run.sh" "$work/e2e/dns/run-case.sh"
(
    cd "$work"
    bash ./e2e/dns/run.sh "${cases[@]}"
) >"$output_dir/matrix.log" 2>&1

printf 'sysnet-linux DNS matrix passed. Evidence: %s\n' "$output_dir"
