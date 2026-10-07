#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_root="$(mktemp -d "${TMPDIR:-/tmp}/almagest-test-e2e-fail-fast.XXXXXXXX")"
trap 'rm -rf "$test_root"' EXIT

mkdir -p "$test_root/bin"
cat >"$test_root/bin/just" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
	failing)
		while [[ ! -e "$TEST_E2E_SUPERVISOR_STATE/slow-started" ]]; do sleep 0.01; done
		exit 7
		;;
	failing-stuck)
		while [[ ! -e "$TEST_E2E_SUPERVISOR_STATE/stuck-started" ]]; do sleep 0.01; done
		exit 9
		;;
	slow)
		trap 'touch "$TEST_E2E_SUPERVISOR_STATE/slow-interrupted"; sleep 0.2; touch "$TEST_E2E_SUPERVISOR_STATE/slow-cleanup-finished"; exit 130' INT TERM
		touch "$TEST_E2E_SUPERVISOR_STATE/slow-started"
		while :; do sleep 0.05; done
		;;
	stuck)
		trap '' INT TERM
		touch "$TEST_E2E_SUPERVISOR_STATE/stuck-started"
		while :; do sleep 0.05; done
		;;
	*) exit 2 ;;
esac
EOF
chmod +x "$test_root/bin/just"

status=0
PATH="$test_root/bin:$PATH" TEST_E2E_SUPERVISOR_STATE="$test_root" \
	"$repo_root/scripts/test-e2e-fail-fast.sh" failing slow \
	>"$test_root/output" 2>&1 || status=$?
if ((status != 7)); then
	printf 'supervisor status = %d, want 7\n' "$status" >&2
	cat "$test_root/output" >&2
	exit 1
fi
if [[ ! -e "$test_root/slow-interrupted" ]]; then
	echo "supervisor did not interrupt the concurrent test" >&2
	cat "$test_root/output" >&2
	exit 1
fi
if [[ ! -e "$test_root/slow-cleanup-finished" ]]; then
	echo "supervisor returned before concurrent cleanup finished" >&2
	cat "$test_root/output" >&2
	exit 1
fi
grep -F 'failing failed; interrupting concurrent tests' "$test_root/output" >/dev/null || {
	echo "supervisor did not report the triggering failure" >&2
	exit 1
}

status=0
PATH="$test_root/bin:$PATH" TEST_E2E_SUPERVISOR_STATE="$test_root" \
	E2E_SIBLING_CLEANUP_TIMEOUT=1 timeout 5 \
	"$repo_root/scripts/test-e2e-fail-fast.sh" failing-stuck stuck \
	>"$test_root/stuck-output" 2>&1 || status=$?
if ((status != 9)); then
	printf 'stuck-cleanup supervisor status = %d, want 9\n' "$status" >&2
	cat "$test_root/stuck-output" >&2
	exit 1
fi
grep -F 'cleanup timed out for stuck; killing its process group' "$test_root/stuck-output" >/dev/null || {
	echo "supervisor did not enforce the sibling cleanup deadline" >&2
	cat "$test_root/stuck-output" >&2
	exit 1
}

echo "e2e parallel fail-fast supervisor tests passed"
