#!/usr/bin/env bash
# Remote paths are deliberately expanded on the host.
# shellcheck disable=SC2016,SC2029,SC2054
set -euo pipefail
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
# shellcheck source=dev/winvm/common.sh
source "$script_dir/common.sh"
mode=${1:-smoke}
[[ $mode == smoke || $mode == test || $mode == resource || $mode == --shell || $mode == --prepare ]] || die 'usage: run.sh [smoke | test | resource | --shell RUN | --prepare DIRECTORY]'
for command in qemu-system-x86_64 qemu-img ssh scp jq python3 timeout flock git tar setsid; do require_command "$command"; done
require_kvm
ensure_cache_dir
artifact_root="$repo_root/artifacts/winvm"
mkdir -p "$artifact_root" "$winvm_cache_dir/locks"
exec 6>"$winvm_cache_dir/locks/vm-run.lock"
flock -w 1800 6 || die 'another VM held the host lock for 30 minutes'
reject_live_qemu_orphan
if [[ $mode == --shell ]]; then
    run_dir=$(realpath -e "${2:?retained run directory required}")
    [[ $run_dir == "$artifact_root"/run-* ]] || die 'run is outside artifact root'
    key=$(jq -er .baseImageKey "$run_dir/run.json")
else
    if [[ -n ${WINVM_PREPARED_DIR:-} ]]; then
        key=$(<"$WINVM_PREPARED_DIR/base-key")
    else
        key=$(base_key)
    fi
    if [[ $mode == --prepare ]]; then
        run_dir=$(realpath -e "${2:?prepared directory required}")
    elif [[ -n ${WINVM_RUN_DIR:-} ]]; then
        run_dir=$(realpath -e "$WINVM_RUN_DIR")
        [[ $run_dir == "$artifact_root"/run-* && ! -e $run_dir/run.json ]] || die 'invalid or used WINVM_RUN_DIR'
    else
        run_dir=$(mktemp -d "$artifact_root/run-XXXXXXXX")
    fi
fi
[[ $key =~ ^[0-9a-f]{64}$ ]] || die 'invalid base image key'
image_dir="$winvm_cache_dir/images/$key"
exec 8>"$winvm_cache_dir/locks/image-$key.lock"
flock -s 8
cached_image_valid "$image_dir" "$key" || die 'base image is absent or invalid; run just winvm-image'
if [[ $mode == --prepare ]]; then
    python3 "$script_dir/package-worktree.py" "$run_dir/source.tar" >"$run_dir/source.sha256"
    "$script_dir/stage.sh" "$run_dir" >"$run_dir/staging.log" 2>&1
    sha256sum "$run_dir/payload.tar" >"$run_dir/payload.sha256"
    printf '%s\n' "$key" >"$run_dir/base-key"
    exit
fi
exec 5>"$run_dir/run.lock"
flock -n 5 || die 'this run is active'
overlay="$run_dir/overlay.qcow2"
vars="$run_dir/OVMF_VARS.fd"
sockets=$(make_socket_dir)
qga="$sockets/qga.sock"
qmp="$sockets/qmp.sock"
stage=setup
progress() { printf '==> Windows VM: %s\n' "$1"; }
# Runtime supervision takes ownership with exec below. Before that point no VM
# exists, so setup failure only needs to retain evidence and remove sockets.
cleanup_setup() {
    local status=$?
    trap - EXIT
    remove_socket_dir "$sockets"
    if [[ $mode != --shell && -f $run_dir/run.json ]]; then
        jq --arg stage "$stage" --argjson code "$status" '.stage=$stage | .exitStatus=$code | .status="failed"' "$run_dir/run.json" >"$run_dir/run.json.new"
        mv "$run_dir/run.json.new" "$run_dir/run.json"
    fi
    exit "$status"
}
trap cleanup_setup EXIT
if [[ $mode != --shell ]]; then
    progress 'preparing isolated test payload'
    if [[ $mode == smoke ]]; then
        suite=harness-smoke
        test_execution=not-run
    elif [[ $mode == test ]]; then
        suite=windows-milestone-3
        test_execution=native
    else
        suite=windows-milestone-8-resource
        test_execution=native-resource
    fi
    jq -n --arg key "$key" --arg suite "$suite" --arg execution "$test_execution" '{schemaVersion:1,baseImageKey:$key,suite:$suite,status:"running",testExecution:$execution}' >"$run_dir/run.json"
    cp "$image_dir/manifest.json" "$run_dir/image-manifest.json"
    qemu-img create -q -f qcow2 -F qcow2 -b "$image_dir/base.qcow2" "$overlay"
    cp "$image_dir/OVMF_VARS.fd" "$vars"
    chmod 0600 "$vars"
    stage=package
    if [[ -n ${WINVM_PREPARED_DIR:-} ]]; then
        for prepared_file in source.tar source.sha256 payload.tar generated.sha256; do
            ln "$WINVM_PREPARED_DIR/$prepared_file" "$run_dir/$prepared_file"
        done
    else
        python3 "$script_dir/package-worktree.py" "$run_dir/source.tar" >"$run_dir/source.sha256"
        "$script_dir/stage.sh" "$run_dir" >"$run_dir/staging.log" 2>&1
    fi
    sha256sum "$run_dir/payload.tar" >"$run_dir/payload.sha256"
    jq --arg source "$(sha256_file "$run_dir/source.tar")" --arg payload "$(sha256_file "$run_dir/payload.tar")" --arg generated "$(sha256_file "$run_dir/generated.sha256")" '.sourceArchiveSHA256=$source | .payloadSHA256=$payload | .generatedManifestSHA256=$generated' "$run_dir/run.json" >"$run_dir/run.json.new"
    mv "$run_dir/run.json.new" "$run_dir/run.json"
fi
actual=$(qemu-img info --output=json "$overlay" | jq -r '."full-backing-filename" // ."backing-filename"')
[[ $(realpath -e "$actual") == "$(realpath -e "$image_dir/base.qcow2")" ]] || die 'overlay backing file mismatch'
allocate_locked_port
port=$winvm_ssh_port
read -r key_type key_data _ <"$image_dir/host-key.pub"
printf '[127.0.0.1]:%s %s %s\n' "$port" "$key_type" "$key_data" >"$run_dir/known_hosts"
qemu=(qemu-system-x86_64 -name almagest-winvm -machine "$(jq -r .machine.type "$config_file")" -cpu "$(jq -r .machine.cpu "$config_file")" -smp "$(jq -r .machine.cpus "$config_file")" -m "$(jq -r .machine.memoryMiB "$config_file")" -display none
    -chardev "file,id=serial0,path=$run_dir/serial.log" -serial chardev:serial0
    -drive "if=pflash,format=raw,readonly=on,file=$(find_ovmf code)" -drive "if=pflash,format=raw,file=$vars"
    -device ich9-ahci,id=sata -drive "if=none,id=osdisk,format=qcow2,file=$overlay" -device ide-hd,drive=osdisk,bus=sata.0
    -device e1000e,netdev=net0 -netdev "user,id=net0,hostfwd=tcp:127.0.0.1:$port-:22"
    -device virtio-serial-pci -chardev "socket,path=$qga,server=on,wait=off,id=qga0" -device virtserialport,chardev=qga0,name=org.qemu.guest_agent.0 -qmp "unix:$qmp,server=on,wait=off")
printf '%q ' "${qemu[@]}" >"$run_dir/qemu-command.log"
# The supervisor owns and reaps QEMU directly. Lock descriptors stay in the
# supervisor, but are not inherited by QEMU or guest transport processes.
soak_args=()
if [[ -n ${ALMAGEST_WINDOWS_SOAK_DURATION:-} ]]; then
    soak_args=(--soak-duration "$ALMAGEST_WINDOWS_SOAK_DURATION")
fi
exec python3 "$script_dir/supervise.py" --mode="$mode" --run-dir "$run_dir" \
    --sockets "$sockets" --port "$port" --key "$(ssh_private_key)" \
    --cache-dir "$winvm_cache_dir" "${soak_args[@]}" \
    -- "${qemu[@]}"
