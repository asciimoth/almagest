#!/usr/bin/env bash
# Reuse a complete keyed image without hashing the large installation media.
# The full builder performs those checks when the keyed image is absent.
set -euo pipefail
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
# shellcheck source=dev/winvm/common.sh
source "$script_dir/common.sh"
for command in qemu-system-x86_64 qemu-img jq ssh-keygen sha256sum; do require_command "$command"; done
ensure_cache_dir
prepare_native_dependencies
ensure_ssh_key
key=$(base_key)
image_dir="$winvm_cache_dir/images/$key"
if cached_image_valid "$image_dir" "$key"; then
    printf 'Reusing Windows base image %s without rehashing installation media\n' "$key"
    exit
fi
exec "$script_dir/build-image.sh"
