#!/usr/bin/env python3

import os
import socket
import subprocess
import sys
import time


def main() -> int:
	expected_mark = int(os.environ.get("E2E_EXPECTED_SOCKET_MARK", "0x414c4d01"), 0)
	timeout = float(os.environ.get("E2E_SOCKET_MARK_TIMEOUT", "60"))
	deadline = time.monotonic() + timeout
	observed_mark = 0

	while time.monotonic() <= deadline:
		with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as probe:
			observed_mark = probe.getsockopt(socket.SOL_SOCKET, socket.SO_MARK)
		if observed_mark == expected_mark:
			return subprocess.call(["/usr/local/bin/direct-fetch-curl", *sys.argv[1:]])
		time.sleep(0.02)

	print(
		"direct-fetch did not receive the DefaultTun bypass mark "
		f"{expected_mark:#010x}; last socket mark was {observed_mark:#010x}",
		file=sys.stderr,
	)
	return 125


if __name__ == "__main__":
	raise SystemExit(main())
