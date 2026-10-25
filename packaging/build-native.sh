#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
output="$repo_root/build/packaging"

if [[ "$(go env GOHOSTOS)" != "linux" || "$(go env GOHOSTARCH)" != "amd64" ]]; then
	printf 'packaging supports only a linux/amd64 build host\n' >&2
	exit 1
fi

mkdir -p "$output"

common_flags=(
	-static-pie
	-O2
	-D_FORTIFY_SOURCE=3
	-fstack-protector-strong
	-fstack-clash-protection
	-fno-strict-overflow
	-Wformat=2
	-Wall
	-Wextra
	-Werror
	'-Wl,-z,relro,-z,now,-z,noexecstack'
)

extra_ldflags=()
if [[ -n "${ALMAGEST_GLIBC_STATIC_LDFLAG:-}" ]]; then
	read -r -a extra_ldflags <<<"$ALMAGEST_GLIBC_STATIC_LDFLAG"
fi
read -r -a seccomp_flags <<<"$(pkg-config --cflags --libs libseccomp)"

gcc "${extra_ldflags[@]}" "${common_flags[@]}" \
	-o "$output/almagest-linux-role-launcher" \
	"$repo_root/bin/native/linux-role/launcher.c" \
	"${seccomp_flags[@]}"

gcc "${extra_ldflags[@]}" "${common_flags[@]}" \
	-o "$output/almagest-subprogram-launcher" \
	"$repo_root/bin/native/subprogram/launcher.c" \
	"${seccomp_flags[@]}"
