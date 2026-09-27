#!/usr/bin/env python3
"""Send a UDP echo request with bounded, scheduling-safe retries."""

from __future__ import annotations

import argparse
import socket
import time


DEFAULT_TIMEOUT = 5.0
DEFAULT_RETRY_INTERVAL = 0.25
DEFAULT_MINIMUM_ATTEMPTS = 4


def request(
    address: str,
    port: int,
    token: bytes,
    *,
    timeout: float = DEFAULT_TIMEOUT,
    retry_interval: float = DEFAULT_RETRY_INTERVAL,
    minimum_attempts: int = DEFAULT_MINIMUM_ATTEMPTS,
) -> int:
    """Return the send count after the expected echo response arrives."""
    if timeout <= 0:
        raise ValueError("timeout must be positive")
    if retry_interval <= 0:
        raise ValueError("retry interval must be positive")
    if minimum_attempts < 1:
        raise ValueError("minimum attempts must be positive")

    family = socket.AF_INET6 if ":" in address else socket.AF_INET
    expected = b"pong:" + token
    deadline = time.monotonic() + timeout
    attempts = 0

    with socket.socket(family, socket.SOCK_DGRAM) as client:
        client.connect((address, port))
        while attempts < minimum_attempts or time.monotonic() < deadline:
            remaining = deadline - time.monotonic()
            if attempts >= minimum_attempts and remaining <= 0:
                break

            # A process can be descheduled after a send. The test fixture
            # discards the first datagram, so the wall deadline must not stop
            # the client before it makes the required retransmissions.
            receive_timeout = retry_interval
            if remaining > 0:
                receive_timeout = min(receive_timeout, remaining)
            client.settimeout(receive_timeout)
            client.send(token)
            attempts += 1
            try:
                response = client.recv(65535)
            except TimeoutError:
                continue
            if response != expected:
                raise RuntimeError(f"unexpected UDP response: {response!r}")
            return attempts

        raise TimeoutError(
            "UDP echo response timed out: "
            f"token={token!r} local={client.getsockname()!r} "
            f"peer={client.getpeername()!r} attempts={attempts}"
        )


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("address")
    parser.add_argument("port", type=int)
    parser.add_argument("token")
    parser.add_argument("--timeout", type=float, default=DEFAULT_TIMEOUT)
    parser.add_argument(
        "--retry-interval",
        type=float,
        default=DEFAULT_RETRY_INTERVAL,
    )
    parser.add_argument(
        "--minimum-attempts",
        type=int,
        default=DEFAULT_MINIMUM_ATTEMPTS,
    )
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    try:
        request(
            args.address,
            args.port,
            args.token.encode(),
            timeout=args.timeout,
            retry_interval=args.retry_interval,
            minimum_attempts=args.minimum_attempts,
        )
    except (OSError, RuntimeError, ValueError) as error:
        raise SystemExit(str(error)) from error


if __name__ == "__main__":
    main()
