#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/../.." && pwd)"
output_dir="${1:-$repo_root/artifacts/linux-pmark-guest/$(date -u +%Y%m%dT%H%M%SZ)-$$}"
mkdir -p "$output_dir"
output_dir="$(cd -- "$output_dir" && pwd)"
work="$(mktemp -d)"
image="almagest-pmark-guest:$(date -u +%Y%m%d%H%M%S)-$$"
cleanup() {
    docker image rm --force "$image" >/dev/null 2>&1 || true
    rm -rf -- "$work"
}
trap cleanup EXIT

# Run the released standalone p-mark guest gate. It covers daemon lifecycle,
# existing-socket reconciliation, exact kernel comm policy, and process-map
# churn without using host PID, network, or BPF namespaces.
pmark_dir="$(go -C "$repo_root/bin" list -m -f '{{.Dir}}' github.com/asciimoth/p-mark)"
mkdir -p "$work/pmark"
cp -a -- "$pmark_dir/." "$work/pmark/"
chmod -R u+w "$work/pmark"
# Go's module cache does not retain executable mode in this dependency release.
# Restore script modes in the disposable copy before using the upstream runner.
find "$work/pmark/e2e" -type f -name '*.sh' -exec chmod 0755 {} +

(
    cd "$work/pmark"
    E2E_GUEST_IMAGE="$image" \
        E2E_GUEST_ARTIFACT_DIR="$output_dir/evidence" \
        E2E_GUEST_CACHE_DIR="$work/cache" \
        bash ./e2e/guest/run.sh
) >"$output_dir/run.log" 2>&1

printf 'p-mark upstream guest checks passed. Evidence: %s\n' "$output_dir"
