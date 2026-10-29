#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
max_age_days=${1:-7}

if [[ ! $max_age_days =~ ^[0-9]+$ ]]; then
	printf 'cleanup: DAYS must be a nonnegative integer\n' >&2
	exit 2
fi

cutoff=$(( $(date +%s) - max_age_days * 86400 ))

is_stale() {
	local modified
	modified=$(stat -c %Y -- "$1")
	((max_age_days == 0 || modified <= cutoff))
}

remove_path() {
	local path=$1
	[[ -e $path || -L $path ]] || return 0
	printf 'Removing %s\n' "$path"
	rm -rf -- "$path"
}

remove_stale_run() {
	local path=$1 lock_fd
	is_stale "$path" || return 0
	if [[ -f $path/run.lock ]]; then
		exec {lock_fd}>"$path/run.lock"
		if ! flock -n "$lock_fd"; then
			printf 'Keeping active run %s\n' "$path"
			exec {lock_fd}>&-
			return 0
		fi
	fi
	remove_path "$path"
	[[ -z ${lock_fd:-} ]] || exec {lock_fd}>&-
}

cd -- "$repo_root"

# Read the same optional overrides as the WinVM harness.
if [[ -f ${WINVM_ENV_FILE:-$repo_root/dev/winvm/env} ]]; then
	set -a
	# shellcheck disable=SC1090
	source "${WINVM_ENV_FILE:-$repo_root/dev/winvm/env}"
	set +a
fi
winvm_cache_dir=${WINVM_CACHE_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/almagest/winvm}

# The flow harness does not use per-run locks. Hold its host-wide lock while
# changing any WinVM artifact or cache. If a VM is active, skip all such data.
winvm_cleanup_allowed=1
winvm_lock_root=${XDG_RUNTIME_DIR:-}
if [[ $winvm_lock_root != /* || ! -d $winvm_lock_root || ! -w $winvm_lock_root || ! -O $winvm_lock_root ]]; then
	winvm_lock_root=/tmp/almagest-winvm-host-$(id -u)
	if ! mkdir -m 0700 -- "$winvm_lock_root" 2>/dev/null; then
		if [[ ! -d $winvm_lock_root || -L $winvm_lock_root || ! -O $winvm_lock_root ]]; then
			printf 'cleanup: unsafe WinVM host lock directory: %s\n' "$winvm_lock_root" >&2
			exit 1
		fi
	fi
	chmod 0700 -- "$winvm_lock_root"
fi
exec {winvm_host_fd}>"$winvm_lock_root/almagest-winvm-host.lock"
if ! flock -n "$winvm_host_fd"; then
	winvm_cleanup_allowed=0
	printf 'Keeping WinVM artifacts and cache because a VM job is active\n'
fi

# These paths contain only generated data and can always be rebuilt.
for path in \
	build dist coverage e2e/coverage \
	panel/node_modules panel/public panel/vendor \
	bin/almagestd/static app/src-tauri/gen app/src-tauri/target app/frontend; do
	remove_path "$repo_root/$path"
done

# Remove Python bytecode without changing source directories.
while IFS= read -r -d '' path; do
	remove_path "$path"
done < <(find "$repo_root" -type d -name __pycache__ -prune -print0)
remove_path "$repo_root/.ruff_cache"

# Retained diagnostics can be useful for recent failures. Age each direct
# artifact independently so a new run does not retain unrelated old runs.
if [[ -d $repo_root/artifacts ]]; then
	while IFS= read -r -d '' path; do
		name=${path##*/}
		if [[ $name == winvm ]]; then
			if ((winvm_cleanup_allowed)); then
				while IFS= read -r -d '' run; do
					remove_stale_run "$run"
				done < <(find "$path" -mindepth 1 -maxdepth 1 -print0)
			fi
		elif [[ $name == test-cycles || $name == e2e || $name == test-resources ]]; then
			while IFS= read -r -d '' run; do
				remove_stale_run "$run"
			done < <(find "$path" -mindepth 1 -maxdepth 1 -print0)
		elif is_stale "$path"; then
			remove_path "$path"
		fi
	done < <(find "$repo_root/artifacts" -mindepth 1 -maxdepth 1 -print0)
	find "$repo_root/artifacts" -depth -type d -empty -delete
fi

# Keep downloaded inputs, SSH keys, and the newest base image for each variant.
if ((winvm_cleanup_allowed)) && [[ $winvm_cache_dir == /* && -d $winvm_cache_dir ]]; then
	mkdir -p -- "$winvm_cache_dir/locks"
	declare -A newest_image=()
	declare -A newest_time=()
	declare -A referenced_image=()

	while IFS= read -r -d '' manifest; do
		key=$(jq -r '.baseImageKey // empty' "$manifest" 2>/dev/null || true)
		[[ $key =~ ^[0-9a-f]{64}$ ]] && referenced_image[$key]=1
	done < <(find "$repo_root/artifacts/winvm" -mindepth 2 -maxdepth 2 \
		-type f -name image-manifest.json -print0 2>/dev/null || true)

	while IFS= read -r -d '' image_dir; do
		manifest=$image_dir/manifest.json
		[[ -f $manifest ]] || continue
		variant=$(jq -r '.imageVariant // empty' "$manifest" 2>/dev/null || true)
		[[ -n $variant ]] || continue
		modified=$(stat -c %Y -- "$manifest")
		if [[ -z ${newest_time[$variant]:-} || $modified -gt ${newest_time[$variant]} ]]; then
			newest_time[$variant]=$modified
			newest_image[$variant]=$image_dir
		fi
	done < <(find "$winvm_cache_dir/images" -mindepth 1 -maxdepth 1 -type d -print0 2>/dev/null || true)

	while IFS= read -r -d '' image_dir; do
		key=${image_dir##*/}
		manifest=$image_dir/manifest.json
		[[ -f $manifest ]] || continue
		variant=$(jq -r '.imageVariant // empty' "$manifest" 2>/dev/null || true)
		[[ -n $variant ]] || continue
		[[ ${newest_image[$variant]:-} != "$image_dir" ]] || continue
		[[ -z ${referenced_image[$key]:-} ]] || continue
		is_stale "$image_dir" || continue
		lock=$winvm_cache_dir/locks/image-$key.lock
		exec {lock_fd}>"$lock"
		if flock -n "$lock_fd"; then
			remove_path "$image_dir"
		else
			printf 'Keeping active WinVM image %s\n' "$image_dir"
		fi
		exec {lock_fd}>&-
	done < <(find "$winvm_cache_dir/images" -mindepth 1 -maxdepth 1 -type d -print0 2>/dev/null || true)

	while IFS= read -r -d '' path; do
		is_stale "$path" || continue
		unset key lock_fd
		name=${path##*/}
		case $name in
			build-*) key=${name#build-}; key=${key%%.*} ;;
			.publish-*) key=${name#.publish-}; key=${key%-*} ;;
		esac
		if [[ ${key:-} =~ ^[0-9a-f]{64}$ ]]; then
			exec {lock_fd}>"$winvm_cache_dir/locks/image-$key.lock"
			if ! flock -n "$lock_fd"; then
				printf 'Keeping active WinVM build %s\n' "$path"
				exec {lock_fd}>&-
				unset lock_fd
				continue
			fi
		fi
		remove_path "$path"
		[[ -z ${lock_fd:-} ]] || exec {lock_fd}>&-
	done < <(find "$winvm_cache_dir" -mindepth 1 -maxdepth 1 \
		\( -name 'build-*' -o -name '.publish-*' \) -print0)
fi

# Remove only images created by this repository. A running container makes
# docker image rm fail, which keeps its image. Do not prune global Docker data.
if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
	mapfile -t docker_images < <(
		docker image ls --format '{{.Repository}}:{{.Tag}}' | awk '
			/^almagest-e2e:/ ||
			/^almagest-package-test-/ ||
			/^almagest-capability-(runtime|experiment):/ ||
			/^almagest-rootless-isolation-probe:/ { print }
		'
	)
	if ((${#docker_images[@]})); then
		printf 'Removing Almagest Docker images\n'
		docker image rm "${docker_images[@]}" || true
	fi
	docker image prune --force --filter label=io.almagest.e2e.input-sha256 >/dev/null || true
fi

printf 'Cleanup complete. Retained artifact age: %s day(s).\n' "$max_age_days"
