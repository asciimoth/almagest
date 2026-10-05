#!/usr/bin/env bash
# Run the isolated two-guest packet-flow gate used by the regular WinVM suite.
# Remote PowerShell paths deliberately expand on the host. Values sourced from
# common.sh are validated there.
# shellcheck disable=SC1091,SC2029,SC2054,SC2154
set -euo pipefail
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
# shellcheck source=dev/winvm/common.sh
source "$script_dir/common.sh"

for command in qemu-system-x86_64 qemu-img ssh scp jq python3 timeout flock go; do require_command "$command"; done
require_kvm /dev/kvm
ensure_cache_dir
artifact_root="$repo_root/artifacts/winvm"
mkdir -p "$artifact_root" "$winvm_cache_dir/locks"
exec 6>"$winvm_cache_dir/locks/vm-run.lock"
flock -w 1800 6 || die 'another VM held the host lock for 30 minutes'
reject_live_qemu_orphan

key=$(base_key)
image_dir="$winvm_cache_dir/images/$key"
exec 8>"$winvm_cache_dir/locks/image-$key.lock"
flock -s 8
cached_image_valid "$image_dir" "$key" || die 'base image is absent or invalid; run just winvm-image'

if [[ -n ${WINVM_FLOW_RUN_DIR:-} ]]; then
    run_dir=$(realpath -e "$WINVM_FLOW_RUN_DIR")
    [[ $run_dir == "$artifact_root"/run-flow-* && ! -e $run_dir/run.json ]] || die 'invalid or used WINVM_FLOW_RUN_DIR'
else
    run_dir=$(mktemp -d "$artifact_root/run-flow-XXXXXXXX")
fi
client_overlay="$run_dir/client-overlay.qcow2"
endpoint_overlay="$run_dir/endpoint-overlay.qcow2"
client_vars="$run_dir/client-OVMF_VARS.fd"
endpoint_vars="$run_dir/endpoint-OVMF_VARS.fd"
client_sockets=$(make_socket_dir)
endpoint_sockets=$(make_socket_dir)
client_pid=''
endpoint_pid=''
stage=setup

stop_guest() {
    local pid=$1 qga=$2 qmp=$3 label=$4
    [[ -n $pid ]] || return 0
    if kill -0 "$pid" 2>/dev/null; then
        "$script_dir/tools/qga.py" --socket "$qga" --timeout 5 shutdown >>"$run_dir/$label-agent.log" 2>&1 || true
        wait_for_pid "$pid" "$(jq -r .machine.shutdownTimeoutSeconds "$config_file")" || \
            "$script_dir/tools/qga.py" --socket "$qmp" --timeout 5 qmp quit >>"$run_dir/$label-agent.log" 2>&1 || true
        stop_and_reap_pid "$pid" 10 10 10 || return 1
    else
        wait "$pid" 2>/dev/null || true
    fi
}

cleanup() {
    local exit_status=$?
    trap - EXIT INT TERM
    stop_guest "$client_pid" "$client_sockets/qga.sock" "$client_sockets/qmp.sock" client || exit_status=1
    stop_guest "$endpoint_pid" "$endpoint_sockets/qga.sock" "$endpoint_sockets/qmp.sock" endpoint || exit_status=1
    remove_socket_dir "$client_sockets"
    remove_socket_dir "$endpoint_sockets"
    if [[ -f $run_dir/run.json ]]; then
        jq --arg stage "$stage" --argjson code "$exit_status" \
            '.stage=$stage | .exitStatus=$code | .status=(if $code==0 then "passed" else "failed" end)' \
            "$run_dir/run.json" >"$run_dir/run.json.new" && mv "$run_dir/run.json.new" "$run_dir/run.json"
    fi
    if ((exit_status == 0)); then
        find "$client_overlay" "$endpoint_overlay" "$client_vars" "$endpoint_vars" \
            -maxdepth 0 -type f -delete
    else
        printf 'WinVM packet-flow gate failed at %s. Artifacts: %s\n' "$stage" "$run_dir" >&2
    fi
    exit "$exit_status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

jq -n --arg key "$key" '{schemaVersion:1,baseImageKey:$key,suite:"windows-batch3-packet-flow",status:"running",stage:"setup"}' >"$run_dir/run.json"
qemu-img create -q -f qcow2 -F qcow2 -b "$image_dir/base.qcow2" "$client_overlay"
qemu-img create -q -f qcow2 -F qcow2 -b "$image_dir/base.qcow2" "$endpoint_overlay"
cp "$image_dir/OVMF_VARS.fd" "$client_vars"
cp "$image_dir/OVMF_VARS.fd" "$endpoint_vars"
chmod 0600 "$client_vars" "$endpoint_vars"

allocate_locked_port
client_port=$winvm_ssh_port
allocate_locked_port
endpoint_port=$winvm_ssh_port
read -r key_type key_data _ <"$image_dir/host-key.pub"
printf '[127.0.0.1]:%s %s %s\n' "$client_port" "$key_type" "$key_data" >"$run_dir/client-known-hosts"
printf '[127.0.0.1]:%s %s %s\n' "$endpoint_port" "$key_type" "$key_data" >"$run_dir/endpoint-known-hosts"

tunnel_socket="$endpoint_sockets/tunnel.sock"
underlay_socket="$endpoint_sockets/underlay.sock"
tunnel_pcap="$run_dir/tunnel.pcap"
underlay_pcap="$run_dir/underlay.pcap"
machine=$(jq -r .machine.type "$config_file")
cpu=$(jq -r .machine.cpu "$config_file")
firmware=$(find_ovmf code)
endpoint_qemu=(qemu-system-x86_64 -name almagest-flow-endpoint -machine "$machine" -cpu "$cpu" -smp 2 -m 4096 -display none
    -chardev "file,id=serial0,path=$run_dir/endpoint-serial.log" -serial chardev:serial0
    -drive "if=pflash,format=raw,readonly=on,file=$firmware" -drive "if=pflash,format=raw,file=$endpoint_vars"
    -device ich9-ahci,id=sata -drive "if=none,id=osdisk,format=qcow2,file=$endpoint_overlay" -device ide-hd,drive=osdisk,bus=sata.0
    -device e1000e,netdev=net0 -netdev "user,id=net0,hostfwd=tcp:127.0.0.1:$endpoint_port-:22"
    -device e1000e,mac=52:54:00:12:34:20,netdev=tunnel -netdev "stream,id=tunnel,server=on,addr.type=unix,addr.path=$tunnel_socket"
    -device e1000e,mac=52:54:00:12:34:21,netdev=underlay -netdev "stream,id=underlay,server=on,addr.type=unix,addr.path=$underlay_socket"
    -device virtio-serial-pci -chardev "socket,path=$endpoint_sockets/qga.sock,server=on,wait=off,id=qga0"
    -device virtserialport,chardev=qga0,name=org.qemu.guest_agent.0 -qmp "unix:$endpoint_sockets/qmp.sock,server=on,wait=off")
client_qemu=(qemu-system-x86_64 -name almagest-flow-client -machine "$machine" -cpu "$cpu" -smp "$(jq -r .machine.cpus "$config_file")" -m "$(jq -r .machine.memoryMiB "$config_file")" -display none
    -chardev "file,id=serial0,path=$run_dir/client-serial.log" -serial chardev:serial0
    -drive "if=pflash,format=raw,readonly=on,file=$firmware" -drive "if=pflash,format=raw,file=$client_vars"
    -device ich9-ahci,id=sata -drive "if=none,id=osdisk,format=qcow2,file=$client_overlay" -device ide-hd,drive=osdisk,bus=sata.0
    -device e1000e,netdev=net0 -netdev "user,id=net0,hostfwd=tcp:127.0.0.1:$client_port-:22"
    -device e1000e,mac=52:54:00:12:34:10,netdev=tunnel -netdev "stream,id=tunnel,server=off,addr.type=unix,addr.path=$tunnel_socket"
    -object "filter-dump,id=tunnel-capture,netdev=tunnel,file=$tunnel_pcap"
    -device e1000e,mac=52:54:00:12:34:11,netdev=underlay -netdev "stream,id=underlay,server=off,addr.type=unix,addr.path=$underlay_socket"
    -object "filter-dump,id=underlay-capture,netdev=underlay,file=$underlay_pcap"
    -device virtio-serial-pci -chardev "socket,path=$client_sockets/qga.sock,server=on,wait=off,id=qga0"
    -device virtserialport,chardev=qga0,name=org.qemu.guest_agent.0 -qmp "unix:$client_sockets/qmp.sock,server=on,wait=off")

printf '%q ' "${endpoint_qemu[@]}" >"$run_dir/endpoint-qemu-command.log"
"${endpoint_qemu[@]}" >>"$run_dir/endpoint-qemu.log" 2>&1 &
endpoint_pid=$!
for _ in {1..100}; do
    [[ -S $tunnel_socket && -S $underlay_socket ]] && break
    kill -0 "$endpoint_pid" 2>/dev/null || die 'endpoint QEMU exited during setup'
    sleep .1
done
[[ -S $tunnel_socket && -S $underlay_socket ]] || die 'endpoint link sockets were not created'
printf '%q ' "${client_qemu[@]}" >"$run_dir/client-qemu-command.log"
"${client_qemu[@]}" >>"$run_dir/client-qemu.log" 2>&1 &
client_pid=$!

ssh_key=$(ssh_private_key)
client_ssh=(-i "$ssh_key" -p "$client_port" -o BatchMode=yes -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=$run_dir/client-known-hosts" -o ConnectTimeout=5)
endpoint_ssh=(-i "$ssh_key" -p "$endpoint_port" -o BatchMode=yes -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=$run_dir/endpoint-known-hosts" -o ConnectTimeout=5)
client_scp=(-i "$ssh_key" -P "$client_port" -o BatchMode=yes -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=$run_dir/client-known-hosts")
endpoint_scp=(-i "$ssh_key" -P "$endpoint_port" -o BatchMode=yes -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=$run_dir/endpoint-known-hosts")

wait_guest() {
    local pid=$1 qga=$2 port=$3 known_hosts=$4 label=$5
    local deadline=$((SECONDS + $(jq -r .machine.bootTimeoutSeconds "$config_file")))
    while ((SECONDS < deadline)); do
        kill -0 "$pid" 2>/dev/null || die "$label QEMU exited during boot"
        if "$script_dir/tools/qga.py" --socket "$qga" --timeout 5 ping >/dev/null 2>&1 && \
            ssh -i "$ssh_key" -p "$port" -o BatchMode=yes -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes \
                -o "UserKnownHostsFile=$known_hosts" -o ConnectTimeout=5 winvm@127.0.0.1 \
                'powershell.exe -NoProfile -Command "if(Test-Path C:\winvm\ready){exit 0}else{exit 1}"' >/dev/null 2>&1; then
            return
        fi
        sleep 2
    done
    die "$label readiness timeout"
}
stage=boot
wait_guest "$client_pid" "$client_sockets/qga.sock" "$client_port" "$run_dir/client-known-hosts" client
wait_guest "$endpoint_pid" "$endpoint_sockets/qga.sock" "$endpoint_port" "$run_dir/endpoint-known-hosts" endpoint

stage=build
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go -C "$repo_root/bin" build -o "$run_dir/flowecho.exe" github.com/asciimoth/sysnet-windows/cmd/flowecho
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go -C "$repo_root/bin" build -o "$run_dir/tunnelpeer.exe" github.com/asciimoth/sysnet-windows/cmd/tunnelpeer
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go -C "$repo_root/bin" build -o "$run_dir/sysnetflow.exe" github.com/asciimoth/sysnet-windows/cmd/sysnetflow

remote="C:/winvm/flow-$BASHPID"
endpoint_remote=${remote//\//\\}
stage=transfer
ssh "${client_ssh[@]}" winvm@127.0.0.1 "powershell.exe -NoProfile -Command \"New-Item -ItemType Directory -Force '$remote'|Out-Null\""
ssh "${endpoint_ssh[@]}" winvm@127.0.0.1 "powershell.exe -NoProfile -Command \"New-Item -ItemType Directory -Force '$remote'|Out-Null\""
scp "${client_scp[@]}" "$run_dir/sysnetflow.exe" "$script_dir/flow-network.ps1" "winvm@127.0.0.1:$remote/" >/dev/null
scp "${endpoint_scp[@]}" "$run_dir/flowecho.exe" "$run_dir/tunnelpeer.exe" "$script_dir/flow-network.ps1" "winvm@127.0.0.1:$remote/" >/dev/null

stage=network
"$script_dir/tools/qga.py" --socket "$endpoint_sockets/qga.sock" --timeout 60 exec powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$endpoint_remote\flow-network.ps1" -Role Endpoint >"$run_dir/endpoint-network.json"
"$script_dir/tools/qga.py" --socket "$client_sockets/qga.sock" --timeout 60 exec powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$endpoint_remote\flow-network.ps1" -Role Client >"$run_dir/client-network.json"
"$script_dir/tools/qga.py" --socket "$endpoint_sockets/qga.sock" --timeout 30 exec powershell.exe -NoProfile -Command \
    "Set-NetFirewallProfile -All -Enabled False; \$echo=New-ScheduledTaskAction -Execute '$endpoint_remote\flowecho.exe'; Register-ScheduledTask -TaskName AlmagestFlowEcho -Action \$echo -User SYSTEM -RunLevel Highest -Force|Out-Null; Start-ScheduledTask AlmagestFlowEcho; \$peer=New-ScheduledTaskAction -Execute '$endpoint_remote\tunnelpeer.exe' -Argument '-listen 198.18.0.1:51900'; Register-ScheduledTask -TaskName AlmagestTunnelPeer -Action \$peer -User SYSTEM -RunLevel Highest -Force|Out-Null; Start-ScheduledTask AlmagestTunnelPeer; Start-Sleep 3" >"$run_dir/endpoint-services.json"
"$script_dir/tools/qga.py" --socket "$client_sockets/qga.sock" --timeout 30 exec powershell.exe -NoProfile -Command \
    "Set-NetFirewallProfile -All -Enabled False; \$t=@(Get-NetAdapter|Where-Object MacAddress -eq '52-54-00-12-34-10'); \$u=@(Get-NetAdapter|Where-Object MacAddress -eq '52-54-00-12-34-11'); if (\$t.Count -ne 1 -or \$u.Count -ne 1) { throw 'flow adapters missing' }; Get-NetRoute -InterfaceIndex \$t[0].ifIndex|Where-Object DestinationPrefix -In '203.0.113.1/32','2001:db8:ffff::1/128'|Remove-NetRoute -Confirm:\$false; New-NetRoute -InterfaceIndex \$u[0].ifIndex -AddressFamily IPv4 -DestinationPrefix '0.0.0.0/0' -NextHop '198.18.1.1' -RouteMetric 5000|Out-Null; New-NetRoute -InterfaceIndex \$u[0].ifIndex -AddressFamily IPv6 -DestinationPrefix '::/0' -NextHop 'fd00:18:1::1' -RouteMetric 5000|Out-Null" >"$run_dir/client-transition.json"

stage='test'
flow_command="\$ErrorActionPreference='Stop'; \$env:SYSNET_WINDOWS_WINTUN_DLL='C:\Windows\System32\wintun.dll'; Start-Service mullvad-split-tunnel; & '$endpoint_remote\sysnetflow.exe' -scenario outnet -output '$endpoint_remote\outnet.jsonl'; if(\$LASTEXITCODE){exit \$LASTEXITCODE}; & '$endpoint_remote\sysnetflow.exe' -scenario split -output '$endpoint_remote\split.jsonl'; \$code=\$LASTEXITCODE; Stop-Service mullvad-split-tunnel -Force; exit \$code"
timeout --foreground 1800 "$script_dir/tools/qga.py" --socket "$client_sockets/qga.sock" --timeout 1800 exec powershell.exe -NoLogo -NoProfile -NonInteractive -Command "$flow_command" >"$run_dir/flow-console.log"

stage=evidence
"$script_dir/tools/qga.py" --socket "$client_sockets/qga.sock" --timeout 30 read "$endpoint_remote\outnet.jsonl" --limit 8388608 >"$run_dir/outnet-observations.jsonl"
"$script_dir/tools/qga.py" --socket "$client_sockets/qga.sock" --timeout 30 read "$endpoint_remote\split.jsonl" --limit 8388608 >"$run_dir/split-observations.jsonl"
python3 - "$run_dir/outnet-observations.jsonl" "$run_dir/split-observations.jsonl" "$run_dir/packet-flow-observations.jsonl" <<'PY'
from pathlib import Path
import sys
Path(sys.argv[3]).write_bytes(Path(sys.argv[1]).read_bytes() + Path(sys.argv[2]).read_bytes())
PY
python3 "$script_dir/tools/flow-pcap.py" --observations "$run_dir/packet-flow-observations.jsonl" \
    --tunnel "$tunnel_pcap" --underlay "$underlay_pcap" --output "$run_dir/packet-flow-evidence.json"
stage=complete
printf 'Windows packet-flow gate passed. Artifacts: %s\n' "$run_dir"
