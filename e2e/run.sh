#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
image="${E2E_IMAGE:-almagest-e2e:local}"

cd "$repo_root"

docker build -f e2e/Dockerfile -t "$image" .

if (($# > 0)); then
	cases=("$@")
else
	case_find_args=(e2e/cases -mindepth 1 -maxdepth 1 -type d)
	if [[ "${E2E_INCLUDE_PRIVILEGED:-}" != "1" && "${E2E_DOCKER_RUN_ARGS:-}" != *"--privileged"* ]]; then
		case_find_args+=( '!' -exec test -e '{}/requires-privileged' ';' )
	fi
	mapfile -t cases < <(find "${case_find_args[@]}" -printf '%f\n' | sort)
fi

if ((${#cases[@]} == 0)); then
	echo "no e2e cases found" >&2
	exit 1
fi

for case_name in "${cases[@]}"; do
	echo "==> e2e: $case_name"
	# shellcheck disable=SC2086
	docker run --rm ${E2E_DOCKER_RUN_ARGS:-} "$image" "$case_name"
done
