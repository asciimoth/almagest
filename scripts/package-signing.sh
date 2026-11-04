#!/usr/bin/env bash
set -euo pipefail

readonly action=${1:?Expected prepare or sign}
readonly output_dir=${2:?Package artifact directory is required}
readonly gpg_command=${GPG_COMMAND:-gpg}
repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
readonly manifest=${ALMAGEST_SIGNING_MANIFEST:-$repo_root/build/packaging/signing-inputs.sha256}
readonly fingerprint=C5C84658CCFD7E8E71DEE933AF3AE54FC3A35C9F

cd "$output_dir"

case $action in
prepare)
	mkdir -p -- "$(dirname -- "$manifest")"
	rm -f -- "$manifest" "$manifest.tmp"
	[[ -f checksums.txt ]] || { printf 'Missing Linux checksums.txt\n' >&2; exit 1; }
	mapfile -t installers < <(find . -maxdepth 1 -type f -name 'almagest_*_windows_amd64.msi' -printf '%f\n')
	if ((${#installers[@]} != 1)); then
		printf 'Expected one Windows MSI, found %d\n' "${#installers[@]}" >&2
		exit 1
	fi
	installer=${installers[0]}
	artifacts=(checksums.txt "$installer" "$installer.sha256")
	while IFS= read -r line; do
		file=${line:66}
		[[ ${line:64:2} == '  ' && $file == almagest* && $file != */* && -f $file ]] || {
			printf 'Invalid Linux checksum entry: %s\n' "$line" >&2
			exit 1
		}
		artifacts+=("$file")
	done < checksums.txt
	((${#artifacts[@]} > 3)) || { printf 'No Linux artifacts found\n' >&2; exit 1; }
	sha256sum --check checksums.txt
	sha256sum --check "$installer.sha256"
	for artifact in "${artifacts[@]}"; do
		[[ -f $artifact ]] || { printf 'Missing package artifact: %s\n' "$artifact" >&2; exit 1; }
	done
	sha256sum -- "${artifacts[@]}" > "$manifest.tmp"
	mv -- "$manifest.tmp" "$manifest"
	printf 'All package artifacts are ready in %s. Run just sign-package when you are ready to touch the hardware token.\n' "$PWD"
	if command -v notify-send >/dev/null 2>&1; then
		notify-send --urgency=critical --expire-time=0 'Almagest packages are ready' \
			'Run just sign-package when you are ready to sign.' >/dev/null 2>&1 || true
	fi
	;;
sign)
	[[ -f $manifest ]] || {
		printf 'No prepared package artifacts. Run just package first.\n' >&2
		exit 1
	}
	sha256sum --check "$manifest"
	mapfile -t artifacts < <(sed -n 's/^[[:xdigit:]]\{64\}  //p' "$manifest")
	((${#artifacts[@]} > 3)) || { printf 'Invalid signing manifest\n' >&2; exit 1; }
	for artifact in "${artifacts[@]}"; do
		"$gpg_command" --batch --yes --local-user "$fingerprint" \
			--output "$artifact.sig" --detach-sign "$artifact"
	done
	printf 'Signed %d package artifacts in %s.\n' "${#artifacts[@]}" "$PWD"
	;;
*) printf 'Expected prepare or sign, got %s\n' "$action" >&2; exit 2 ;;
esac
