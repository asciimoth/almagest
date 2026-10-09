#!/usr/bin/env python3
"""Validate rejection evidence, never production data-path approval."""

import json
import sys


REQUIRED = {
    "restricted_runtime",
    "tcp_duplicate_survives_broker_close",
    "socket_control_denied",
    "tun_ioctls_denied",
    "listener_accept_denied",
    "connected_udp_destination_override",
    "connected_udp_survives_broker_close",
    "unconnected_udp_survives_broker_close",
    "shared_file_flags_mutable",
    "shared_metadata_and_payload_mutable_after_validation",
    "shared_notification_saturated",
    "shared_backing_truncated",
    "tun_survives_broker_close",
    "listener_survives_broker_close",
    "tun_removed_after_runtime_exit",
    "listener_released_after_runtime_exit",
    "tcp_eof_after_runtime_exit",
}


def validate(result):
    if result.get("schema_version") != 1 or result.get("profile") != "linux-runtime-r-v1":
        raise ValueError("invalid security evidence identity")
    checks = result.get("checks", {})
    if set(checks) != REQUIRED:
        raise ValueError("incomplete or unexpected security checks")
    if any(value is not True for value in checks.values()):
        raise ValueError("a security observation failed")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        raise SystemExit("usage: security-evidence.py LINUX.json")
    with open(sys.argv[1], encoding="utf-8") as source:
        validate(json.load(source))
    print("Linux data-path rejection evidence validated")
