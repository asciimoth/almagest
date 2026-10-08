#!/usr/bin/env bash
set -euo pipefail

readonly mode=${1:?usage: test-windows.sh ordinary|privileged}
case "$mode" in
ordinary | privileged) ;;
*) printf 'test-windows: invalid mode: %s\n' "$mode" >&2; exit 2 ;;
esac

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
if [[ $(go env GOHOSTOS) == windows ]]; then
    artifact_dir=${WINDOWS_NATIVE_ARTIFACT_DIR:-$repo_root/artifacts/windows-native}
    mkdir -p "$artifact_dir"
    continue_after_failure=()
    if [[ ${WINVM_FAIL_FAST:-1} == 0 ]]; then
        continue_after_failure=(-ContinueAfterFailure)
    fi
    selected_cases=()
    if [[ -n ${WINVM_TEST_CASES:-} ]]; then
        selected_cases=(-CaseIDs "${WINVM_TEST_CASES//,/~}")
    fi
    powershell.exe -NoProfile -ExecutionPolicy Bypass -File \
        "$repo_root/dev/winvm/test-suite.ps1" \
        -Mode "$mode" -SourceDir "$repo_root" -ArtifactDir "$artifact_dir" \
        "${continue_after_failure[@]}" "${selected_cases[@]}"
else
    "$repo_root/dev/winvm/run.sh" "$mode"
fi
