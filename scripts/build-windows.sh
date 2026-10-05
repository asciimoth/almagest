#!/usr/bin/env bash

set -euo pipefail

source_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly source_root
minimum_go_version="$(awk '$1 == "go" { print $2; exit }' "${source_root}/bin/go.mod")"
readonly minimum_go_version
readonly artifact_root="${WINDOWS_BUILD_DIR:-${source_root}/artifacts/windows}"
readonly modules=(certcat lib bin)
readonly tag_sets=(default almagest_e2e)

fail() {
	printf 'build-windows: %s\n' "$*" >&2
	exit 1
}

check_tool() {
	command -v "$1" >/dev/null 2>&1 || fail "required tool not found: $1"
}

check_go_version() {
	local go_version oldest
	go_version="$(go env GOVERSION)"
	go_version="${go_version#go}"
	if [[ ! "$go_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
		fail "cannot compare Go version ${go_version}; Go ${minimum_go_version} or newer is required"
	fi
	oldest="$(printf '%s\n%s\n' "$minimum_go_version" "$go_version" | sort -V | head -n 1)"
	[[ "$oldest" == "$minimum_go_version" ]] || \
		fail "Go ${minimum_go_version} or newer is required; found ${go_version}"
}

check_arch() {
	case "$1" in
	amd64 | arm64) ;;
	*) fail "unsupported architecture '$1'; use amd64 or arm64" ;;
	esac
}

prepare_workspace() {
	if [[ ! -f "${source_root}/go.work" ]]; then
		(cd "$source_root" && go work init)
	fi
	(cd "$source_root" && go work use ./lib ./bin ./certcat)
}

generate_inputs() {
	if [[ ! -f "${source_root}/certcat/data/machine.json" ]]; then
		fail "certcat/data/machine.json is absent; restore this tracked generator input"
	fi
	go -C "${source_root}/certcat" run ./cmd/certcat-generate \
		-manual data/manual.json -machine data/machine.json -out catalog_gen.go
	(cd "${source_root}/panel" && npm ci && npm run build)
}

record_generated_input_hashes() {
	local manifest="${artifact_root}/generated-inputs.sha256"
	mkdir -p "$artifact_root"
	(
		cd "$source_root"
		git ls-files -z -- \
			certcat/data/manual.json \
			certcat/data/machine.json \
			certcat/cmd/certcat-generate \
			certcat/internal/catalog \
			certcat/go.mod \
			certcat/go.sum \
			panel \
			| LC_ALL=C sort -z \
			| xargs -0 sha256sum
	) >"$manifest"
	[[ -s "$manifest" ]] || fail "generated-input hash manifest is empty"
}

verify_modules() {
	local module
	for module in "${modules[@]}"; do
		go -C "${source_root}/${module}" mod verify
		go -C "${source_root}/${module}" mod tidy -diff
	done
}

package_artifact_name() {
	local package="$1"
	package="${package#github.com/asciimoth/almagest/}"
	package="${package//\//_}"
	printf '%s.test.exe\n' "$package"
}

go_for_target() {
	local arch="$1"
	shift
	GOOS=windows GOARCH="$arch" CGO_ENABLED=0 "$@"
}

compile_tag_set() {
	local arch="$1" tag_set="$2" tag_arg=()
	local module package package_file test_dir test_package_file
	if [[ "$tag_set" != default ]]; then
		tag_arg=(-tags "$tag_set")
	fi

	for module in "${modules[@]}"; do
		package_file="${artifact_root}/${arch}/packages-${tag_set}-${module}.txt"
		go_for_target "$arch" go -C "${source_root}/${module}" list \
			"${tag_arg[@]}" -f '{{.ImportPath}}' ./... >"$package_file"
		[[ -s "$package_file" ]] || fail "no ${module} packages found for windows/${arch} (${tag_set})"

		go_for_target "$arch" go -C "${source_root}/${module}" build \
			"${tag_arg[@]}" ./...

		test_dir="${artifact_root}/${arch}/tests/${tag_set}/${module}"
		mkdir -p "$test_dir"
		test_package_file="${artifact_root}/${arch}/test-packages-${tag_set}-${module}.txt"
		go_for_target "$arch" go -C "${source_root}/${module}" list \
			"${tag_arg[@]}" \
			-f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./... \
			| sed '/^$/d' >"$test_package_file"
		while IFS= read -r package; do
			go_for_target "$arch" go -C "${source_root}/${module}" test \
				"${tag_arg[@]}" -c -o "${test_dir}/$(package_artifact_name "$package")" "$package"
		done <"$test_package_file"
	done
}

write_evidence() {
	local arch="$1" input_manifest_hash go_version
	input_manifest_hash="$(sha256sum "${artifact_root}/generated-inputs.sha256" | awk '{print $1}')"
	go_version="$(go env GOVERSION)"
	cat >"${artifact_root}/${arch}/build-evidence.json" <<EOF
{
  "schemaVersion": 1,
  "target": {"os": "windows", "arch": "${arch}", "cgoEnabled": false},
  "toolchain": {"go": "${go_version}", "minimumGo": "go${minimum_go_version}"},
  "modules": ["certcat", "lib", "bin"],
  "buildTags": ["default", "almagest_e2e"],
  "generatedInputs": {
    "manifest": "../../generated-inputs.sha256",
    "manifestSHA256": "${input_manifest_hash}"
  },
  "build": "passed",
  "testCompilation": "passed",
  "testExecution": "not-run"
}
EOF
}

build_arch() {
	local arch="$1" tag_set
	printf 'Building Windows artifacts for %s\n' "$arch"
	mkdir -p "${artifact_root}/${arch}/bin"

	for tag_set in "${tag_sets[@]}"; do
		compile_tag_set "$arch" "$tag_set"
	done

	go_for_target "$arch" go -C "${source_root}/bin" build \
		-o "${artifact_root}/${arch}/bin/almagestd.exe" ./almagestd
	go_for_target "$arch" go -C "${source_root}/bin" build \
		-o "${artifact_root}/${arch}/bin/almagest-cli.exe" ./almagest-cli
	write_evidence "$arch"
}

main() {
	local arch
	check_tool go
	check_tool git
	check_tool node
	check_tool npm
	check_tool sha256sum
	check_go_version

	[[ "$#" -gt 0 ]] || set -- amd64 arm64
	for arch in "$@"; do
		check_arch "$arch"
	done

	prepare_workspace
	generate_inputs
	record_generated_input_hashes
	verify_modules
	for arch in "$@"; do
		build_arch "$arch"
	done
	printf 'Windows build artifacts: %s\n' "$artifact_root"
}

main "$@"
