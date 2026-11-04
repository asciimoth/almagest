#!/usr/bin/env bash
set -euo pipefail

output_dir=${1:?Package output directory is required}
cd "$output_dir"
[[ -f checksums.txt ]] || { printf 'Missing checksums.txt\n' >&2; exit 1; }

declare -A publish_files=([checksums.txt]=1)
while IFS= read -r line; do
	file=${line:66}
	[[ ${line:64:2} == '  ' && $file == almagest* && $file != */* && -f $file ]] || {
		printf 'Invalid package checksum entry: %s\n' "$line" >&2
		exit 1
	}
	publish_files["$file"]=1
done < checksums.txt
((${#publish_files[@]} > 1)) || { printf 'No package artifacts found\n' >&2; exit 1; }
sha256sum --check checksums.txt

while IFS= read -r -d '' path; do
	name=${path#./}
	[[ -n ${publish_files[$name]:-} ]] || rm -rf -- "$path"
done < <(find . -mindepth 1 -maxdepth 1 -print0)
