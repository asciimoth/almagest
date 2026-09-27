#!/usr/bin/env python3

from __future__ import annotations

import os
from pathlib import Path
import queue
import re
import signal
import socket
import subprocess
import sys
import threading
import time
import unittest
from collections.abc import Callable


LIB_DIR = Path(__file__).resolve().parent
sys.path.insert(0, str(LIB_DIR))

import udp_echo_client  # noqa: E402


class UDPEchoClientTest(unittest.TestCase):
    def test_retries_after_first_datagram_is_lost(self) -> None:
        server = self.make_server(socket.AF_INET, "127.0.0.1")
        token = b"retry-v4"

        def serve() -> None:
            first, _ = server.recvfrom(65535)
            self.assertEqual(first, token)
            second, peer = server.recvfrom(65535)
            self.assertEqual(second, token)
            server.sendto(b"pong:" + second, peer)

        worker, errors = self.start_server(serve)
        attempts = udp_echo_client.request(
            "127.0.0.1",
            server.getsockname()[1],
            token,
            timeout=1,
            retry_interval=0.02,
            minimum_attempts=2,
        )
        self.assertEqual(attempts, 2)
        self.finish_server(worker, errors)
        server.close()

    @unittest.skipUnless(socket.has_ipv6, "IPv6 is not available")
    def test_retries_ipv6(self) -> None:
        try:
            server = self.make_server(socket.AF_INET6, "::1")
        except OSError as error:
            self.skipTest(f"IPv6 loopback is not available: {error}")
        token = b"retry-v6"

        def serve() -> None:
            server.recvfrom(65535)
            second, peer = server.recvfrom(65535)
            server.sendto(b"pong:" + second, peer)

        worker, errors = self.start_server(serve)
        attempts = udp_echo_client.request(
            "::1",
            server.getsockname()[1],
            token,
            timeout=1,
            retry_interval=0.02,
            minimum_attempts=2,
        )
        self.assertEqual(attempts, 2)
        self.finish_server(worker, errors)
        server.close()

    @unittest.skipUnless(hasattr(signal, "SIGSTOP"), "SIGSTOP is not available")
    def test_scheduler_pause_after_first_send_does_not_consume_retry_budget(
        self,
    ) -> None:
        server = self.make_server(socket.AF_INET, "127.0.0.1")
        token = "scheduler-pause"
        command = [
            sys.executable,
            str(LIB_DIR / "udp_echo_client.py"),
            "127.0.0.1",
            str(server.getsockname()[1]),
            token,
            "--timeout",
            "0.1",
            "--retry-interval",
            "0.2",
            "--minimum-attempts",
            "2",
        ]
        client = subprocess.Popen(
            command,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
        try:
            first, _ = server.recvfrom(65535)
            self.assertEqual(first, token.encode())
            os.kill(client.pid, signal.SIGSTOP)
            time.sleep(0.2)
            os.kill(client.pid, signal.SIGCONT)
            second, peer = server.recvfrom(65535)
            self.assertEqual(second, token.encode())
            server.sendto(b"pong:" + second, peer)
            _, stderr = client.communicate(timeout=2)
            self.assertEqual(client.returncode, 0, stderr)
        finally:
            if client.poll() is None:
                os.kill(client.pid, signal.SIGCONT)
                client.kill()
                client.wait()
            server.close()

    def test_timeout_reports_attempt_count_and_endpoints(self) -> None:
        server = self.make_server(socket.AF_INET, "127.0.0.1")
        with self.assertRaisesRegex(TimeoutError, r"local=.*peer=.*attempts=") as caught:
            udp_echo_client.request(
                "127.0.0.1",
                server.getsockname()[1],
                b"no-response",
                timeout=0.03,
                retry_interval=0.01,
                minimum_attempts=3,
            )
        match = re.search(r"attempts=(\d+)", str(caught.exception))
        self.assertIsNotNone(match)
        self.assertGreaterEqual(int(match.group(1)), 3)
        server.close()

    def test_rejects_unexpected_response(self) -> None:
        server = self.make_server(socket.AF_INET, "127.0.0.1")

        def serve() -> None:
            _, peer = server.recvfrom(65535)
            server.sendto(b"wrong", peer)

        worker, errors = self.start_server(serve)
        with self.assertRaisesRegex(RuntimeError, "unexpected UDP response"):
            udp_echo_client.request(
                "127.0.0.1",
                server.getsockname()[1],
                b"unexpected",
                timeout=1,
                retry_interval=0.02,
            )
        self.finish_server(worker, errors)
        server.close()

    def make_server(self, family: int, address: str) -> socket.socket:
        server = socket.socket(family, socket.SOCK_DGRAM)
        server.settimeout(2)
        server.bind((address, 0))
        return server

    def start_server(
        self,
        target: Callable[[], None],
    ) -> tuple[threading.Thread, queue.Queue[BaseException]]:
        errors: queue.Queue[BaseException] = queue.Queue()

        def run() -> None:
            try:
                target()
            except BaseException as error:
                errors.put(error)

        worker = threading.Thread(target=run, daemon=True)
        worker.start()
        return worker, errors

    def finish_server(
        self,
        worker: threading.Thread,
        errors: queue.Queue[BaseException],
    ) -> None:
        worker.join(1)
        self.assertFalse(worker.is_alive())
        if not errors.empty():
            raise errors.get_nowait()


if __name__ == "__main__":
    unittest.main()
