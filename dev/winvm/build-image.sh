#!/usr/bin/env bash
# shellcheck disable=SC2054
set -euo pipefail
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
# shellcheck source=dev/winvm/common.sh
source "$script_dir/common.sh"
variant=${1:-standard}
[[ $variant == standard || $variant == package-clean ]] || die 'usage: build-image.sh [standard | package-clean]'
"$script_dir/doctor.sh"
download_packages
prepare_native_dependencies
ensure_ssh_key
key=$(base_key "$variant")
image_dir="$winvm_cache_dir/images/$key"
ssh_dir="$winvm_cache_dir/ssh"
mkdir -p "$winvm_cache_dir/images" "$winvm_cache_dir/locks" "$ssh_dir"
chmod 0700 "$winvm_cache_dir" "$ssh_dir"
# Use the same lock as test runs. Windows installation is memory-intensive and
# must not race another guest on this host.
acquire_vm_host_lock
reject_live_qemu_orphan
exec 9>"$winvm_cache_dir/locks/image-$key.lock"
printf 'Waiting for image lock %s...\n' "$key"
flock 9
if cached_image_valid "$image_dir" "$key"; then
    printf 'Reusing Windows base image %s\n' "$key"
    exit
fi
ssh_key=$(ssh_private_key)
work=$(mktemp -d "$winvm_cache_dir/build-$key.XXXXXX")
sockets=$(make_socket_dir)
qga="$sockets/qga.sock"
qmp="$sockets/qmp.sock"
pid=''
interrupted=0
cleanup() {
    status=$?
    trap - EXIT INT TERM
    stopped=1
    if [[ -n $pid ]]; then
        if ((status != 0 && interrupted == 0)); then
            timeout 20 "$script_dir/tools/qga.py" --socket "$qga" --timeout 15 exec powershell.exe -NoProfile -Command 'Get-Content C:\winvm\provision.log -Tail 100 -ErrorAction SilentlyContinue; Get-Service' >"$work/diagnostics.log" 2>&1 || true
        fi
        "$script_dir/tools/qga.py" --socket "$qga" --timeout 5 shutdown >/dev/null 2>&1 || true
        grace=20
        ((interrupted)) && grace=5
        if ! stop_and_reap_pid "$pid" "$grace" 10 10; then
            record_qemu_orphan "$pid"
            stopped=0
            status=1
        fi
    fi
    if ((stopped)); then remove_socket_dir "$sockets"; else printf 'winvm: QEMU survived cleanup; retaining image files and sockets\n' >&2; fi
    ((status == 0)) || printf 'winvm: failed image files remain in %s; retry: just winvm-image\n' "$work" >&2
    exit "$status"
}
trap cleanup EXIT
trap 'interrupted=1; exit 130' INT
trap 'interrupted=1; exit 143' TERM
cp "$script_dir/Autounattend.xml" "$work/Autounattend.xml"
cp "$script_dir/provision.ps1" "$work/provision.ps1"
cp "$ssh_key.pub" "$work/authorized_key.pub"
password=$(python3 -c 'import secrets; print(secrets.token_hex(24)+"aA!")')
sed -i -e "s|@@WINDOWS_IMAGE_NAME@@|$(jq -r .windowsImageName "$config_file")|g" -e "s|@@ADMIN_PASSWORD@@|$password|g" "$work/Autounattend.xml"
unset password
sed -i \
    -e "s|@@IMAGE_VARIANT@@|$variant|g" \
    -e "s|@@INSTALL_RUNTIME_DEPENDENCIES@@|$([[ $variant == standard ]] && printf 'true' || printf 'false')|g" \
    -e "s|@@GO_FILE@@|$(locked_value .go.file)|g" \
    -e "s|@@OPENSSH_FILE@@|$(locked_value .openssh.file)|g" \
    -e "s|@@TOR_FILE@@|$(locked_value .tor.file)|g" \
    -e "s|@@DOTNET_FILE@@|$(locked_value .dotnet.file)|g" \
    -e "s|@@PYTHON_FILE@@|$(locked_value .python.file)|g" \
    -e "s|@@RUST_FILE@@|$(locked_value .rust.file)|g" \
    -e "s|@@LLVM_MINGW_FILE@@|$(locked_value .llvmMingw.file)|g" \
    -e "s|@@WIX_FILE@@|$(locked_value .wix.file)|g" \
    -e "s|@@WIX_UTIL_FILE@@|$(locked_value .wixUtil.file)|g" \
    -e "s|@@WIX_DIFXAPP_FILE@@|$(locked_value .wixDifxApp.file)|g" \
    -e "s|@@WIX_DTF_FILE@@|$(locked_value .wixDtf.file)|g" \
    -e "s|@@WIX_UI_FILE@@|$(locked_value .wixUi.file)|g" "$work/provision.ps1"
for section in go openssh tor dotnet python rust llvmMingw wix wixUtil wixDifxApp wixDtf wixUi; do
    cp "$(package_path "$section")" "$work/"
done
mkdir "$work/driver" "$work/wintun"
cp "$winvm_driver_dir"/* "$work/driver/"
cp "$winvm_wintun_dir/amd64/wintun.dll" "$work/wintun/wintun-amd64.dll"
cp "$winvm_wintun_dir/arm64/wintun.dll" "$work/wintun/wintun-arm64.dll"
printf 'Creating the Windows provisioning ISO...\n'
xorriso -as mkisofs -quiet -J -R -V WINVM_PROVISION -o "$work/provision.iso" \
    "$work/Autounattend.xml" "$work/provision.ps1" "$work/authorized_key.pub" \
    "$work/$(locked_value .go.file)" "$work/$(locked_value .openssh.file)" \
    "$work/$(locked_value .tor.file)" "$work/$(locked_value .dotnet.file)" \
    "$work/$(locked_value .python.file)" "$work/$(locked_value .rust.file)" \
    "$work/$(locked_value .llvmMingw.file)" \
    "$work/$(locked_value .wix.file)" "$work/$(locked_value .wixUtil.file)" \
    "$work/$(locked_value .wixDifxApp.file)" "$work/$(locked_value .wixDtf.file)" \
    "$work/$(locked_value .wixUi.file)" \
    "$work/driver" "$work/wintun"
qemu-img create -q -f qcow2 "$work/base.qcow2" "$(jq -r .machine.diskGiB "$config_file")G"
cp "$(find_ovmf vars)" "$work/OVMF_VARS.fd"
chmod 0600 "$work/OVMF_VARS.fd"
allocate_locked_port
port=$winvm_ssh_port
qemu=(qemu-system-x86_64 -name almagest-winvm-build -machine "$(jq -r .machine.type "$config_file")" -cpu "$(jq -r .machine.cpu "$config_file")" -smp "$(jq -r .machine.cpus "$config_file")" -m "$(jq -r .machine.memoryMiB "$config_file")" -display none -chardev "file,id=serial0,path=$work/serial.log" -serial chardev:serial0 -drive "if=pflash,format=raw,readonly=on,file=$(find_ovmf code)" -drive "if=pflash,format=raw,file=$work/OVMF_VARS.fd" -device ich9-ahci,id=sata -drive "if=none,id=osdisk,format=qcow2,file=$work/base.qcow2" -device ide-hd,drive=osdisk,bus=sata.0 -drive "if=none,id=windows,media=cdrom,readonly=on,file=$(input_path windows)" -device ide-cd,drive=windows,bus=sata.1 -drive "if=none,id=virtio,media=cdrom,readonly=on,file=$(input_path virtio)" -device ide-cd,drive=virtio,bus=sata.2 -drive "if=none,id=provision,media=cdrom,readonly=on,file=$work/provision.iso" -device ide-cd,drive=provision,bus=sata.3 -device e1000e,netdev=net0 -netdev "user,id=net0,hostfwd=tcp:127.0.0.1:$port-:22" -device virtio-serial-pci -chardev "socket,path=$qga,server=on,wait=off,id=qga0" -device virtserialport,chardev=qga0,name=org.qemu.guest_agent.0 -qmp "unix:$qmp,server=on,wait=off" -boot once=d,menu=off)
printf 'Starting the unattended Windows installation. This usually takes several minutes.\n'
printf 'Detailed logs: %s\n' "$work"
printf '%q ' "${qemu[@]}" >"$work/qemu-command.log"
"${qemu[@]}" >>"$work/qemu.log" 2>&1 &
pid=$!
deadline=$((SECONDS + 20))
qmp_ready=0
while ((SECONDS < deadline)); do
    kill -0 "$pid" || die 'QEMU exited before QMP readiness'
    if [[ -S $qmp ]] && "$script_dir/tools/qga.py" --socket "$qmp" --timeout 3 \
        qmp query-status >/dev/null 2>&1; then
        qmp_ready=1
        break
    fi
    sleep .1
done
((qmp_ready)) || die 'QMP readiness timeout'
# Firmware and optical-media timing varies across cold boots. Send the Return
# key until setup starts writing to the disk, then stop immediately. This avoids
# both a fixed early window and stray input after Windows Setup has started.
disk_size_before_boot=$(stat -c %s "$work/base.qcow2")
setup_started=0
for _ in {1..60}; do
    kill -0 "$pid" || die 'QEMU exited during the installation boot window'
    "$script_dir/tools/qga.py" --socket "$qmp" --timeout 3 qmp send-key \
        --arguments '{"keys":[{"type":"qcode","data":"ret"}]}' >/dev/null
    sleep 1
    disk_size=$(stat -c %s "$work/base.qcow2")
    if ((disk_size >= disk_size_before_boot + 16 * 1024 * 1024)); then
        setup_started=1
        break
    fi
done
((setup_started)) || die 'Windows Setup did not start writing to the guest disk'
install_started=$SECONDS
install_timeout=$(jq -r .machine.installTimeoutSeconds "$config_file")
deadline=$((SECONDS + install_timeout))
next_progress=$((SECONDS + 30))
token=''
printf 'Windows Setup is writing the system disk. Waiting for guest provisioning...\n'
while ((SECONDS < deadline)); do
    kill -0 "$pid" || die 'QEMU exited during installation'
    if "$script_dir/tools/qga.py" --socket "$qga" --timeout 5 ping >/dev/null 2>&1; then
        failure=$("$script_dir/tools/qga.py" --socket "$qga" --timeout 10 exec powershell.exe -NoProfile -Command 'if(Test-Path C:\winvm\provision.failed){Get-Content -Raw C:\winvm\provision.failed}' 2>/dev/null || true)
        [[ -z $failure ]] || die "Windows provisioning failed: $failure"
        token=$(ready_token "$qga" 2>/dev/null || true)
        [[ -n $token ]] && break
    fi
    if ((SECONDS >= next_progress)); then
        printf 'Still provisioning Windows: %d seconds elapsed, about %d minutes remain.\n' "$((SECONDS - install_started))" "$(((deadline - SECONDS + 59) / 60))"
        next_progress=$((SECONDS + 30))
    fi
    sleep 5
done
[[ -n $token ]] || die 'Windows provisioning timeout'
printf 'Guest provisioning completed. Verifying readiness and SSH identity...\n'
# Require the same atomic readiness generation after a quiet period. This prevents
# a transient QGA response or a reboot boundary from publishing an incomplete image.
sleep 5
token2=$(ready_token "$qga")
[[ $token2 == "$token" ]] || die 'guest readiness generation changed'
"$script_dir/tools/qga.py" --socket "$qga" --timeout 30 exec powershell.exe -NoProfile -Command 'Get-Content -Raw C:\winvm\manifest.json' >"$work/guest.json"
jq -e . "$work/guest.json" >/dev/null
# Obtain the host key through the private guest-agent socket, not the network.
"$script_dir/tools/qga.py" --socket "$qga" --timeout 10 exec powershell.exe -NoProfile -Command 'Get-Content C:\ProgramData\ssh\ssh_host_ed25519_key.pub' >"$work/host-key.pub"
ssh-keygen -l -f "$work/host-key.pub" >/dev/null
read -r key_type key_data _ <"$work/host-key.pub"
printf '[127.0.0.1]:%s %s %s\n' "$port" "$key_type" "$key_data" >"$work/known_hosts"
timeout --foreground 30 ssh -i "$ssh_key" -p "$port" -o BatchMode=yes -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=$work/known_hosts" -o ConnectTimeout=5 winvm@127.0.0.1 'whoami' >"$work/ssh-identity.log"
"$script_dir/tools/qga.py" --socket "$qga" --timeout 10 shutdown || true
wait_for_pid "$pid" "$(jq -r .machine.shutdownTimeoutSeconds "$config_file")" || die 'provisioned guest did not stop'
wait "$pid" || true
pid=''
jq -n --arg key "$key" --argjson guest "$(cat "$work/guest.json")" '{baseImageKey:$key}+$guest' >"$work/manifest.json"
publish="$winvm_cache_dir/images/.publish-$key-$$"
mkdir "$publish"
mv "$work/base.qcow2" "$work/OVMF_VARS.fd" "$work/manifest.json" "$work/host-key.pub" "$publish/"
chmod 0444 "$publish"/*
if [[ -e $image_dir ]]; then
    [[ $image_dir == "$winvm_cache_dir/images/$key" ]] || die 'unsafe image directory'
    find "$image_dir" -depth -delete
fi
mv "$publish" "$image_dir"
find "$work" -depth -delete
printf 'Built Windows base image %s\n' "$key"
