#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
mode=${1:-}
web_port=8090
endpoint="web://127.0.0.1:${web_port}"

case "$mode" in
    '')
        cd "$repo_root/app"
        exec cargo tauri dev
        ;;
    daemon)
        just --justfile "$repo_root/justfile" --working-directory "$repo_root" build
        daemon_command=("$repo_root/scripts/run-daemon.sh" '')
        ;;
    daemon-sudo)
        just --justfile "$repo_root/justfile" --working-directory "$repo_root" build-isolation-linux
        sudo -v
        daemon_command=("$repo_root/scripts/run-daemon.sh" --sudo '')
        ;;
    *)
        printf 'unsupported app-debug mode: %s\n' "$mode" >&2
        printf '%s\n' 'usage: just app-debug [daemon|daemon-sudo]' >&2
        exit 2
        ;;
esac

setsid "${daemon_command[@]}" &
daemon_pid=$!

# shellcheck disable=SC2329 # Invoked through the EXIT trap.
cleanup() {
    status=$?
    trap - EXIT
    if kill -0 "$daemon_pid" 2>/dev/null; then
        kill -INT -- "-$daemon_pid" 2>/dev/null || true
    fi
    wait "$daemon_pid" 2>/dev/null || true
    exit "$status"
}
trap cleanup EXIT

ready=0
for _ in {1..1200}; do
    if ! kill -0 "$daemon_pid" 2>/dev/null; then
        set +e
        wait "$daemon_pid"
        daemon_status=$?
        set -e
        if [[ $daemon_status == 0 ]]; then
            daemon_status=1
        fi
        printf 'development daemon stopped before its web admin API was ready\n' >&2
        exit "$daemon_status"
    fi
    if curl --fail --silent --output /dev/null "http://127.0.0.1:${web_port}/"; then
        ready=1
        break
    fi
    sleep 0.25
done

if [[ $ready != 1 ]]; then
    printf 'timed out waiting for the web admin API at %s\n' "$endpoint" >&2
    exit 1
fi

cd "$repo_root/app"
ALMAGEST_API_URL="$endpoint" cargo tauri dev
