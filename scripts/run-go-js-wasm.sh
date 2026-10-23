#!/usr/bin/env bash

set -euo pipefail

wasm_go_root="$(go env GOROOT)"
wasm_runner="${wasm_go_root}/lib/wasm/go_js_wasm_exec"

if [[ ! -x "${wasm_runner}" ]]; then
	printf 'WASM test runner is not executable: %s\n' "${wasm_runner}" >&2
	exit 1
fi

# Node passes its environment into the Go WASM runtime. A large development
# environment can exceed the runtime limit before the test starts.
exec env -i PATH="${PATH}" TMPDIR="${TMPDIR:-/tmp}" "${wasm_runner}" "$@"
