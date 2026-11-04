#!/usr/bin/env bash
# Variables below are set by common.sh.
# shellcheck disable=SC2154
set -euo pipefail
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
# shellcheck source=dev/winvm/common.sh
# shellcheck disable=SC1091,SC2154
source "$script_dir/../dev/winvm/common.sh"
# shellcheck source=dev/winvm/storage-space.sh
# shellcheck disable=SC1091
source "$winvm_dir/storage-space.sh"
[[ -z ${WINVM_PREPARED_DIR:-} ]] || die 'unset WINVM_PREPARED_DIR for a release; the MSI must use the current source'
for command in qemu-system-x86_64 qemu-img ssh scp ssh-keygen jq python3 timeout flock git tar setsid; do
    require_command "$command"
done
require_kvm /dev/kvm
"$winvm_dir/doctor.sh" --validate
[[ -r $(ssh_private_key) ]] || die 'VM SSH private key is absent; run just winvm-image'
private_public=$(ssh-keygen -y -P '' -f "$(ssh_private_key)")
read -r key_type key_data _ <"$(ssh_public_key)"
[[ $private_public == "$key_type $key_data" || $private_public == "$key_type $key_data "* ]] || die 'VM SSH keys do not match; run just winvm-image'
prepare_native_dependencies
for file in "$SYSNET_WINDOWS_WINTUN_DIR/licenses/wintun-LICENSE.txt" \
    "$MULLVAD_SPLIT_TUNNEL_DRIVER_DIR/licenses/mullvad-LICENSE-MPL.txt"; do
    [[ -r $file ]] || die "package license is absent: $file"
done
key=$(base_key standard)
cached_image_valid "$winvm_cache_dir/images/$key" "$key" || die 'base image is absent or invalid; run just winvm-image'
find_ovmf code >/dev/null || die 'OVMF code firmware is absent'
find_ovmf vars >/dev/null || die 'OVMF variable firmware is absent'
configured_memory_mib=$(jq -r .machine.memoryMiB "$config_file")
memory_mib=${WINVM_MEMORY_MIB:-$configured_memory_mib}
[[ $memory_mib =~ ^[0-9]+$ && $memory_mib -ge 4096 && $memory_mib -le $configured_memory_mib ]] || \
    die "WINVM_MEMORY_MIB must be between 4096 and $configured_memory_mib"
available_mib=$(awk '/^MemAvailable:/ {print int($2/1024)}' /proc/meminfo)
((available_mib >= memory_mib)) || die "VM needs $memory_mib MiB available memory"
require_vm_space "$repo_root" 10 'VM package artifacts'
