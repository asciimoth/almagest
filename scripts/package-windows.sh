#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)

resolve_version() {
	local requested=${1:-} tag
	if [[ -n $requested ]]; then
		printf '%s\n' "$requested"
		return
	fi
	if [[ -n ${ALMAGEST_PACKAGE_VERSION:-} ]]; then
		printf '%s\n' "$ALMAGEST_PACKAGE_VERSION"
		return
	fi
	if tag=$(git -C "$repo_root" describe --tags --exact-match HEAD 2>/dev/null); then
		tag=${tag#v}
		if [[ $tag =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
			printf '%s\n' "$tag"
			return
		fi
	fi
	printf '0.0.0\n'
}

if [[ ${1:-} == --print-version ]]; then
	resolve_version "${2:-}"
	exit
fi

version=$(resolve_version "${1:-}")
[[ $version =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || {
	printf 'Windows package version must be an MSI x.y.z version: %s\n' "$version" >&2
	exit 2
}
output_dir=${2:-$repo_root/dist}
mkdir -p "$repo_root/artifacts/winvm" "$output_dir"
run_dir=$(mktemp -d "$repo_root/artifacts/winvm/run-package-build-XXXXXXXX")

printf 'Building unsigned Windows MSI version %s\n' "$version"
ALMAGEST_PACKAGE_VERSION=$version WINVM_RUN_DIR=$run_dir \
	"$repo_root/dev/winvm/run.sh" package

name="almagest_${version}_windows_amd64"
artifact_dir="$run_dir/package"
msi="$artifact_dir/$name.msi"
checksum="$msi.sha256"
evidence="$artifact_dir/package-build.json"
for path in "$msi" "$checksum" "$evidence"; do
	[[ -f $path ]] || {
		printf 'Windows package output is absent: %s\n' "$path" >&2
		exit 1
	}
done

(cd "$artifact_dir" && sha256sum --check "$(basename "$checksum")")
install -m 0644 "$msi" "$output_dir/$name.msi"
install -m 0644 "$checksum" "$output_dir/$name.msi.sha256"
install -m 0644 "$evidence" "$output_dir/$name.build.json"
install -m 0644 "$repo_root/packaging/windows/README.md" \
	"$output_dir/WINDOWS_PACKAGE_CONTENTS.md"
printf 'Windows package artifacts: %s\n' "$output_dir"
