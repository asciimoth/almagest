#!/usr/bin/env bash
set -euo pipefail

readonly fingerprint=C5C84658CCFD7E8E71DEE933AF3AE54FC3A35C9F
readonly output_dir=${1:?Windows artifact directory is required}
readonly gpg_command=${GPG_COMMAND:-gpg}

mapfile -t installers < <(
	find "$output_dir" -maxdepth 1 -type f \
		-name 'almagest_*_windows_amd64.msi' -print
)
if ((${#installers[@]} != 1)); then
	printf 'Expected one Windows MSI in %s, found %d\n' \
		"$output_dir" "${#installers[@]}" >&2
	exit 1
fi

installer=${installers[0]}
base=${installer%.msi}
artifacts=(
	"$installer"
	"$installer.sha256"
	"$base.build.json"
	"$output_dir/WINDOWS_PACKAGE_CONTENTS.md"
	"$output_dir/collect-windows-support.ps1"
)

for artifact in "${artifacts[@]}"; do
	[[ -f $artifact ]] || {
		printf 'Windows release artifact is absent: %s\n' "$artifact" >&2
		exit 1
	}
	"$gpg_command" --batch --local-user "$fingerprint" \
		--output "$artifact.sig" --detach-sign "$artifact"
done
