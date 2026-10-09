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


def validate_data_path(profile, data_path, log_mode):
    if data_path.get("schemaVersion") != 1 or data_path.get("runs", 0) < 7:
        raise ValueError(f"incomplete {profile} data-path evidence")
    if data_path.get("logMode") != log_mode:
        raise ValueError(f"incorrect {profile} logging mode")
    measurements = data_path.get("measurements", [])
    candidates = {item.get("candidate") for item in measurements}
    required_candidates = {
        "in-process-native", "broker-relay", "shared-memory-relay",
        "in-process-native-matcher", "bounded-matcher-rpc",
    }
    allowed_candidates = required_candidates | {"broker-configured-direct-handle"}
    if not required_candidates <= candidates or not candidates <= allowed_candidates:
        raise ValueError(f"invalid {profile} data-path candidates: {sorted(candidates)}")
    if any(not isinstance(item.get("droppedPackets"), int)
           or item["droppedPackets"] < 0 for item in measurements):
        raise ValueError(f"invalid {profile} packet-loss evidence")
    if not data_path.get("matcherMalformedRejected"):
        raise ValueError(f"{profile} malformed matcher request was not rejected")
    if not data_path.get("matcherFreshTupleAccepted"):
        raise ValueError(f"{profile} valid fresh tuple was not accepted")
    if not data_path.get("matcherNoObservationLease"):
        raise ValueError(f"{profile} matcher used an observation lease")
    if "controlResponseP99NS" not in data_path or data_path["controlResponseP99NS"] < 0:
        raise ValueError(f"{profile} control response evidence is absent")
    if not data_path.get("saturatedDataCompleted"):
        raise ValueError(f"{profile} saturated data workload did not complete")
    if profile == "service":
        tun = [item for item in data_path.get("measurements", []) if item.get("transport") == "tun"]
        if {item.get("family") for item in tun} != {"ipv4", "ipv6"}:
            raise ValueError("service Wintun families are incomplete")
        if {item.get("workload") for item in tun} != {"plain", "wireguard-shaped"}:
            raise ValueError("service Wintun workloads are incomplete")


def validate(path, require_data_path=False):
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
            if require_data_path:
                validate_data_path(profile, result.get("dataPath") or {}, "normal")
                for log_mode in ("disabled", "overloaded"):
                    extra = load(archive, prefix + f"result-{log_mode}.json")
                    if extra.get("profile") != profile:
                        raise ValueError(f"invalid {profile} {log_mode} result identity")
                    validate_data_path(profile, extra.get("dataPath") or {}, log_mode)
            if environment.get("launcherElevated") is not elevated:
                raise ValueError(f"incorrect {profile} launcher authority")
            if environment.get("build") != 20348 or environment.get("architecture") != "amd64":
                raise ValueError(f"unsupported {profile} environment")


def main():
    arguments = [value for value in sys.argv[1:] if value != "--require-data-path"]
    if len(arguments) != 1:
        raise SystemExit(
            "usage: isolation-evidence.py GUEST-ARTIFACTS.tar [--require-data-path]"
        )
    validate(arguments[0], "--require-data-path" in sys.argv[1:])
    print("Windows isolation evidence passed")


if __name__ == "__main__":
    main()
