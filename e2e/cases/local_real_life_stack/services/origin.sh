#!/usr/bin/env bash
set -euo pipefail

: "${E2E_ORIGIN_NAME:?}"
runtime="/tmp/almagest-$E2E_ORIGIN_NAME-origin"
mkdir -p "$runtime"

python3 -u - "$E2E_ORIGIN_NAME" "$runtime/access.log" <<'PY' \
	>"$runtime/server.log" 2>&1 &
import http.server
import sys

name, access_log = sys.argv[1:]

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        source = self.client_address[0]
        destination = self.connection.getsockname()[0]
        body = (
            f"{name} origin destination={destination} "
            f"source={source} path={self.path}"
        ).encode()
        with open(access_log, "a", encoding="utf-8") as out:
            out.write(body.decode() + "\n")
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        return

http.server.ThreadingHTTPServer(("0.0.0.0", 80), Handler).serve_forever()
PY
server_pid="$!"

cleanup() {
	local status="$?"
	trap - EXIT INT TERM
	if ((status != 0)); then
		tail -120 "$runtime/server.log" >&2 || true
	fi
	kill "$server_pid" 2>/dev/null || true
	wait "$server_pid" 2>/dev/null || true
	exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

for _ in $(seq 1 120); do
	if curl -fsS http://127.0.0.1/ready >/dev/null 2>&1; then
		touch /tmp/origin-ready
		wait "$server_pid"
		exit $?
	fi
	sleep 0.25
done

echo "$E2E_ORIGIN_NAME origin did not become ready" >&2
exit 1
