#!/usr/bin/env bash
# Space checks do not change the guest image, so keep them outside its cache key.
# shellcheck disable=SC2154 # common.sh supplies the paths before this file is sourced.

require_vm_space() {
    local directory=$1 required_gib=$2 label=$3 available_kib
    available_kib=$(df -Pk "$directory" | awk 'NR==2{print $4}')
    [[ $available_kib =~ ^[0-9]+$ ]] || die "cannot measure free space for $label"
    ((available_kib >= required_gib * 1024 * 1024)) || die "$label needs $required_gib GiB free"
}

check_vm_storage_space() {
    local key='' reusable=0 artifact_directory="$repo_root/artifacts" disk
    if [[ -r $(ssh_public_key) ]]; then
        key=$(base_key)
        if cached_image_valid "$winvm_cache_dir/images/$key" "$key"; then
            reusable=1
        fi
    fi
    if ((reusable)); then
        # Test overlays and collected payloads live in the repository, not in
        # the base-image cache. qcow2 allocates changed blocks as needed.
        [[ -d $artifact_directory ]] || artifact_directory=$repo_root
        require_vm_space "$artifact_directory" 10 'VM test artifacts'
        printf 'Reusing a valid VM base: checked 10 GiB of artifact headroom.\n'
    else
        # A fresh installation can fill its complete configured guest disk.
        # Reserve that capacity plus input/provisioning overhead.
        disk=$(jq -r .machine.diskGiB "$config_file")
        require_vm_space "$winvm_cache_dir" "$((disk + 10))" 'VM image build cache'
    fi
}
