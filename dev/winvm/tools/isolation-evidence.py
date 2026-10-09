#!/usr/bin/env python3
"""Validate checkpoint 11 evidence retrieved from the disposable VM."""

import json
import sys
import tarfile


REQUIRED = {
    "package_init_host_file_denied", "appcontainer_token", "restricted_token",
    "low_integrity", "dep_enabled", "job_membership",
    "private_storage_write", "host_file_denied", "parent_token_denied",
    "process_creation_denied", "driver_control_denied", "native_network_denied",
    "unlisted_handle_denied", "wintun_open_denied",
    "winsock_duplicate_target_denied", "ipc_write",
}


def load(archive, suffix):
    matches = [m for m in archive.getmembers() if m.name.endswith(suffix)]
    if len(matches) != 1:
        raise ValueError(f"expected one {suffix}, found {len(matches)}")
    return json.load(archive.extractfile(matches[0]))


def validate(path):
    with tarfile.open(path) as archive:
        for profile, elevated in (("caller", False), ("service", True)):
            prefix = f"windows-isolation-{profile}/"
            result = load(archive, prefix + "result.json")
            environment = load(archive, prefix + "environment.json")
            if result.get("schemaVersion") != 1 or result.get("profile") != profile:
                raise ValueError(f"invalid {profile} result identity")
            checks = result.get("checks", {})
            if set(checks) != REQUIRED:
                raise ValueError(f"invalid {profile} check set: {set(checks) ^ REQUIRED}")
            failed = [name for name, value in checks.items() if not value.get("ok")]
            if failed:
                raise ValueError(f"failed {profile} checks: {failed}")
            if environment.get("launcherElevated") is not elevated:
                raise ValueError(f"incorrect {profile} launcher authority")
            if environment.get("build") != 20348 or environment.get("architecture") != "amd64":
                raise ValueError(f"unsupported {profile} environment")


def main():
    if len(sys.argv) != 2:
        raise SystemExit("usage: isolation-evidence.py GUEST-ARTIFACTS.tar")
    validate(sys.argv[1])
    print("Windows isolation evidence passed")


if __name__ == "__main__":
    main()
