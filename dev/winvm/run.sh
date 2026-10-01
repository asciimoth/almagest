#!/usr/bin/env bash
# Remote paths are deliberately expanded on the host.
# shellcheck disable=SC2029,SC2054
set -euo pipefail
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
# shellcheck source=dev/winvm/common.sh
source "$script_dir/common.sh"
mode=${1:-smoke}
[[ $mode == smoke || $mode == test || $mode == --shell || $mode == --prepare ]] || die 'usage: run.sh [smoke | test | --shell RUN | --prepare DIRECTORY]'
for command in qemu-system-x86_64 qemu-img ssh scp jq python3 timeout flock git tar; do require_command "$command"; done
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
pid=''
privileged_pid=''
interrupted=0
stage=setup
progress() { printf '==> Windows VM: %s\n' "$1"; }
stream_qga_progress() {
    local command_pid=$1 guest_dir=$2 local_file=$3 shown=0 status=0 guest_file
    : >"$local_file"
    while true; do
        printf -v guest_file '%s/%03d.txt' "$guest_dir" "$shown"
        if "$script_dir/tools/qga.py" --socket "$qga" --timeout 5 read "$guest_file" >"$local_file.event" 2>>"$run_dir/agent.log" && [[ -s $local_file.event ]]; then
            tee -a "$local_file" <"$local_file.event"
            rm -f "$local_file.event"
            shown=$((shown + 1))
            continue
        else
            rm -f "$local_file.event"
        fi
        kill -0 "$command_pid" 2>/dev/null || break
        sleep 2
    done
    wait "$command_pid" || status=$?
    privileged_pid=''
    return "$status"
}
cleanup() {
    status=$?
    trap - EXIT INT TERM
    if [[ -n $privileged_pid ]]; then
        kill "$privileged_pid" 2>/dev/null || true
        wait "$privileged_pid" 2>/dev/null || true
    fi
    if [[ -n $pid ]]; then
        if ((status != 0 && interrupted == 0)); then
            timeout 20 "$script_dir/tools/qga.py" --socket "$qga" --timeout 15 exec powershell.exe -NoProfile -Command 'Get-Process; Get-Service; Get-Content C:\winvm\provision.log -Tail 100 -ErrorAction SilentlyContinue' >"$run_dir/diagnostics.log" 2>&1 || true
            # Test failures can occur before the normal artifact collection step.
            if [[ -n ${remote:-} && ! -f $run_dir/guest-artifacts.tar ]]; then
                timeout --foreground 30 ssh "${ssh_opts[@]}" "$target" "tar.exe -cf $remote/artifacts.tar -C $remote artifacts" >>"$run_dir/diagnostics.log" 2>&1 &&
                    timeout --foreground 60 scp "${scp_opts[@]}" "$target:$remote/artifacts.tar" "$run_dir/guest-artifacts.tar" >>"$run_dir/diagnostics.log" 2>&1 || true
            fi
        fi
        "$script_dir/tools/qga.py" --socket "$qga" --timeout 5 shutdown >>"$run_dir/agent.log" 2>&1 || true
        if ((interrupted)); then
            if ! stop_and_reap_pid "$pid" 5 5 5; then
                record_qemu_orphan "$pid"
                status=1
                stage=shutdown
            else
                remove_socket_dir "$sockets"
            fi
        else
            if ! wait_for_pid "$pid" "$(jq -r .machine.shutdownTimeoutSeconds "$config_file")"; then
                status=1
                stage=shutdown
            fi
            if ! stop_and_reap_pid "$pid" 0 10 10; then
                record_qemu_orphan "$pid"
                status=1
                stage=shutdown
            else
                remove_socket_dir "$sockets"
            fi
        fi
    else
        remove_socket_dir "$sockets"
    fi
    if [[ $mode != --shell && -f $run_dir/run.json ]]; then
        jq --arg stage "$stage" --argjson code "$status" '.stage=$stage | .exitStatus=$code | .status=(if $code==0 then "passed" else "failed" end)' "$run_dir/run.json" >"$run_dir/run.json.new"
        mv "$run_dir/run.json.new" "$run_dir/run.json"
    fi
    if ((status != 0)); then
        printf 'VM failed at %s. Diagnostics: %s\nRecovery: just winvm-shell %q\n' "$stage" "$run_dir" "$run_dir" >&2
    else
        printf 'VM artifacts: %s\n' "$run_dir"
    fi
    exit "$status"
}
trap cleanup EXIT
trap 'interrupted=1; exit 130' INT
trap 'interrupted=1; exit 143' TERM
if [[ $mode != --shell ]]; then
    progress 'preparing isolated test payload'
    if [[ $mode == smoke ]]; then
        suite=harness-smoke
        test_execution=not-run
    else
        suite=windows-milestone-3
        test_execution=native
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
qemu=(qemu-system-x86_64 -name almagest-winvm -machine "$(jq -r .machine.type "$config_file")" -cpu host -smp "$(jq -r .machine.cpus "$config_file")" -m "$(jq -r .machine.memoryMiB "$config_file")" -display none
    -chardev "file,id=serial0,path=$run_dir/serial.log" -serial chardev:serial0
    -drive "if=pflash,format=raw,readonly=on,file=$(find_ovmf code)" -drive "if=pflash,format=raw,file=$vars"
    -device ich9-ahci,id=sata -drive "if=none,id=osdisk,format=qcow2,file=$overlay" -device ide-hd,drive=osdisk,bus=sata.0
    -device e1000e,netdev=net0 -netdev "user,id=net0,hostfwd=tcp:127.0.0.1:$port-:22"
    -device virtio-serial-pci -chardev "socket,path=$qga,server=on,wait=off,id=qga0" -device virtserialport,chardev=qga0,name=org.qemu.guest_agent.0 -qmp "unix:$qmp,server=on,wait=off")
printf '%q ' "${qemu[@]}" >"$run_dir/qemu-command.log"
"${qemu[@]}" >"$run_dir/qemu.log" 2>&1 &
pid=$!
ssh_opts=(-i "$(ssh_private_key)" -p "$port" -o BatchMode=yes -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=$run_dir/known_hosts" -o ConnectTimeout=5)
ssh_opts+=(-o LogLevel=ERROR -o WarnWeakCrypto=no)
scp_opts=(-q -i "$(ssh_private_key)" -P "$port" -o BatchMode=yes -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=$run_dir/known_hosts" -o ConnectTimeout=5 -o LogLevel=ERROR -o WarnWeakCrypto=no)
target=winvm@127.0.0.1
stage=boot
progress 'starting guest and waiting for readiness'
wait_ready "$pid" "$(jq -r .machine.bootTimeoutSeconds "$config_file")" "$qga" "$run_dir/agent.log"
timeout --foreground 30 ssh "${ssh_opts[@]}" "$target" whoami >"$run_dir/ssh-identity.log"
if [[ $mode == --shell ]]; then
    ssh "${ssh_opts[@]}" -t "$target" powershell.exe
    exit
fi
remote="C:/winvm/runs/${run_dir##*/}"
stage=transfer
progress 'transferring test payload'
timeout --foreground 30 ssh "${ssh_opts[@]}" "$target" "powershell.exe -NoProfile -Command \"New-Item -ItemType Directory -Force '$remote/source','$remote/artifacts' | Out-Null\""
timeout --foreground 120 scp "${scp_opts[@]}" "$run_dir/payload.tar" "$target:$remote/payload.tar"
payload_hash=$(sha256_file "$run_dir/payload.tar")
timeout --foreground 120 ssh "${ssh_opts[@]}" "$target" "powershell.exe -NoProfile -Command \"if ((Get-FileHash '$remote/payload.tar').Hash -ne '$payload_hash') { exit 1 }; tar.exe -xf '$remote/payload.tar' -C '$remote/source'; exit \$LASTEXITCODE\""
test_timeout=$(jq -r .machine.testTimeoutSeconds "$config_file")
if [[ $mode == smoke ]]; then
    stage=smoke
    progress 'running smoke checks'
    timeout --foreground "$test_timeout" ssh "${ssh_opts[@]}" "$target" "powershell.exe -NoProfile -ExecutionPolicy Bypass -File $remote/source/dev/winvm/smoke.ps1 -ArtifactDir $remote/artifacts" >"$run_dir/console.log" 2>&1
else
    tests_status=0
    stage=ordinary-tests
    progress 'running ordinary-user tests'
    timeout --foreground "$test_timeout" ssh "${ssh_opts[@]}" "$target" "powershell.exe -NoProfile -ExecutionPolicy Bypass -File $remote/source/dev/winvm/test-suite.ps1 -Mode ordinary -SourceDir $remote/source -ArtifactDir $remote/artifacts" 2>&1 | tee "$run_dir/ordinary-tests.log" || tests_status=$?
    stage=privileged-tests
    progress 'running privileged tests'
    timeout --foreground "$test_timeout" "$script_dir/tools/qga.py" --socket "$qga" --timeout "$test_timeout" exec powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$remote/source/dev/winvm/test-suite.ps1" -Mode privileged -SourceDir "$remote/source" -ArtifactDir "$remote/artifacts" >"$run_dir/privileged-tests.log" 2>&1 &
    privileged_pid=$!
    stream_qga_progress "$privileged_pid" "$remote/artifacts/windows-m4-privileged/progress" "$run_dir/privileged-progress.log" || tests_status=$?
fi
stage=artifacts
progress 'collecting and validating artifacts'
timeout --foreground 30 ssh "${ssh_opts[@]}" "$target" "tar.exe -cf $remote/artifacts.tar -C $remote artifacts"
timeout --foreground 60 scp "${scp_opts[@]}" "$target:$remote/artifacts.tar" "$run_dir/guest-artifacts.tar"
if [[ $mode == smoke ]]; then
    validate_smoke_artifact "$run_dir/guest-artifacts.tar"
else
    if ((tests_status != 0)); then
        stage=tests
        exit "$tests_status"
    fi
    validate_native_test_artifact "$run_dir/guest-artifacts.tar"
    python3 "$script_dir/combine-coverage.py" "$run_dir/guest-artifacts.tar" "$repo_root/coverage/winvm"
fi
stage=shutdown
progress 'shutting down guest'
