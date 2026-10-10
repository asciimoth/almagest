#!/usr/bin/env bash
set -euo pipefail
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/../.." && pwd)"
run_id="$(date -u +%Y%m%dT%H%M%SZ)-$$"
output_dir="${1:-$repo_root/artifacts/linux-profiles/$run_id}"
if [[ -z "$output_dir" ]]; then output_dir="$repo_root/artifacts/linux-profiles/$run_id"; fi
image="almagest-linux-profiles:${run_id,,}"
mkdir -p "$output_dir"
trap 'docker image rm --force "$image" >/dev/null 2>&1 || true' EXIT
docker build -f "$script_dir/Dockerfile" -t "$image" "$repo_root" >"$output_dir/build.log" 2>&1
args=(--rm --network none --memory 512m --memory-swap 512m --pids-limit 128 --cpus 1
    --cap-drop ALL --cap-add CHOWN --cap-add SYS_ADMIN --cap-add SETUID
    --cap-add SETGID --cap-add SETPCAP)
for rights in ordinary net-admin; do
    extra=()
    if [[ "$rights" == net-admin ]]; then extra=(--cap-add NET_ADMIN --cap-add NET_BIND_SERVICE); fi
    docker run "${args[@]}" --security-opt seccomp=unconfined "${extra[@]}" -e ALMAGEST_PROFILE_TEST_NATIVE=1 "$image" \
        /qualification/profile.test -test.v -test.timeout=120s >"$output_dir/$rights.log" 2>&1
done
# Trusted N preserves ordinary caller authority too. This does not qualify a
# rootless R sandbox; the restricted launcher below must still reject it.
docker run --rm --network none --memory 512m --memory-swap 512m --pids-limit 128 --cpus 1 \
    --cap-drop ALL --user 65534:65534 --ulimit nofile=64:4096 \
    -e ALMAGEST_PROFILE_TEST_NATIVE=1 "$image" \
    /qualification/profile.test -test.run '^TestNativePrepareBackend$' -test.v \
    -test.timeout=30s >"$output_dir/backend-ordinary-user.log" 2>&1
# A private cgroup namespace makes controller changes disposable. This case
# never joins the host PID namespace and does not touch host network state.
docker run --rm --network none --memory 512m --memory-swap 512m --pids-limit 128 --cpus 1 \
    --privileged --cgroupns private -e ALMAGEST_PROFILE_CGROUP_PROVISION=1 "$image" \
    /qualification/profile.test -test.run '^TestNativeProvisionRoleCgroups$' -test.v \
    -test.timeout=30s >"$output_dir/cgroup-provision.log" 2>&1
docker run --rm --network none --memory 512m --memory-swap 512m --pids-limit 128 --cpus 1 \
    --cap-drop ALL --cap-add CHOWN --cap-add SETUID --cap-add SETGID --cap-add SETPCAP \
    --security-opt seccomp=unconfined -e ALMAGEST_PROFILE_TEST_NATIVE=1 \
    -e ALMAGEST_PROFILE_EXPECT_UNAVAILABLE=1 "$image" /qualification/profile.test \
    -test.run '^TestNativeRequiredRestrictions$' -test.v >"$output_dir/missing-setup-rights.log" 2>&1
docker run "${args[@]}" --security-opt "seccomp=$script_dir/no-landlock.json" \
    -e ALMAGEST_PROFILE_TEST_NATIVE=1 -e ALMAGEST_PROFILE_EXPECT_UNAVAILABLE=1 \
    "$image" /qualification/profile.test -test.run '^TestNativeRequiredRestrictions$' \
    -test.v >"$output_dir/missing-landlock.log" 2>&1
docker run "${args[@]}" --security-opt seccomp=unconfined --cap-add KILL --cap-add NET_ADMIN --cap-add NET_BIND_SERVICE --device /dev/net/tun \
    -e ALMAGEST_PROFILE_TEST_NATIVE=1 -e ALMAGEST_PROFILE_TRUSTED_SYSTEM=1 "$image" \
    /qualification/profile.test -test.run '^TestNativeTrusted(System|Composition)$' -test.v \
    -test.timeout=90s >"$output_dir/trusted-system.log" 2>&1
docker run "${args[@]}" --security-opt seccomp=unconfined -e ALMAGEST_PROFILE_TEST_NATIVE=1 "$image" \
    /qualification/profile.test -test.run '^$' -test.fuzz '^FuzzNativeArguments$' \
    -test.fuzztime=30s -test.parallel=2 -test.fuzzcachedir=/tmp/profile-fuzz >"$output_dir/native-fuzz.log" 2>&1
docker run --rm --network none --cap-drop ALL --user 65534:65534 "$image" \
    /qualification/launcher v1 runtime 65534 65534 0 1024 268435456 536870912 128 100000 \
    >"$output_dir/rootless.log" 2>&1 && { echo 'Rootless launch was incorrectly accepted'; exit 1; }
rg --quiet -- '--no-isolation' "$output_dir/rootless.log"
docker run --rm --network none "$image" sh -ec '
    go test -C bin -race -cover ./internal/isolation/linuxprofile ./internal/isolation/commandhelper ./internal/isolation/qualification ./internal/isolation/network ./internal/isolation/runtime ./internal/isolation/transport
    go test -C bin ./internal/isolation/linuxprofile -run "^$" -fuzz "^FuzzPlan$" -fuzztime=30s -parallel=2
    go test -C bin ./internal/isolation/linuxprofile -run "^$" -fuzz "^FuzzBackendCgroup$" -fuzztime=30s -parallel=2
    go test -C bin ./internal/isolation/network -run "^$" -fuzz "^FuzzResolverPacket$" -fuzztime=30s -parallel=2
    go test -C bin ./internal/isolation/network -run "^$" -fuzz "^FuzzApplicationDNSResponse$" -fuzztime=30s -parallel=2
' >"$output_dir/portable.log" 2>&1
# Provider and p-mark parity need different disposable environments. DNS uses
# isolated Docker networks for its local service fixtures. Kernel process
# tracing uses a VM with no network device or host PID namespace. The upstream
# runner uses KVM when available and otherwise uses QEMU TCG.
bash "$script_dir/run-dns-matrix.sh" "$output_dir/dns-matrix"
bash "$script_dir/run-pmark-guest.sh" "$output_dir/pmark-guest"
{
    git -C "$repo_root" rev-parse HEAD
    docker version --format '{{.Server.Version}}'
    uname -srmo
} >"$output_dir/environment.txt"
printf 'Linux role profile checks passed. Evidence: %s\n' "$output_dir"
