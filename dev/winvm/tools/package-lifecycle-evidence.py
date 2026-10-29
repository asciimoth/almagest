#!/usr/bin/env python3
"""Validate and extract WinVM MSI package lifecycle evidence."""

import argparse
import hashlib
import json
import tarfile
from pathlib import Path, PurePosixPath


def safe_artifact(member: tarfile.TarInfo) -> bool:
    path = PurePosixPath(member.name)
    return (not path.is_absolute() and ".." not in path.parts and
            path.parts[0] == "artifacts" and
            ((len(path.parts) == 1 and member.isdir()) or
             (len(path.parts) == 2 and member.isfile())))


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("archive", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    with tarfile.open(args.archive) as archive:
        members = archive.getmembers()
        assert members and all(safe_artifact(member) for member in members)
        base = json.load(archive.extractfile("artifacts/package-build.json"))
        upgrade = json.load(archive.extractfile("artifacts/package-upgrade-build.json"))
        smoke = json.load(archive.extractfile("artifacts/package-smoke.json"))
        prepare = json.load(archive.extractfile("artifacts/package-lifecycle-prepare.json"))
        lifecycle = json.load(archive.extractfile("artifacts/package-lifecycle.json"))
        base_msi = archive.extractfile(f"artifacts/{base['package']}").read()
        upgrade_msi = archive.extractfile(f"artifacts/{upgrade['package']}").read()

        assert prepare["schemaVersion"] == lifecycle["schemaVersion"] == 1
        assert prepare["suite"] == "windows-msi-package-lifecycle-prepare"
        assert prepare["outcome"] == "passed"
        assert lifecycle["suite"] == "windows-msi-package-lifecycle"
        assert lifecycle["outcome"] == "passed"
        assert prepare["baseVersion"] == base["version"] == lifecycle["baseVersion"]
        assert prepare["upgradeVersion"] == upgrade["version"] == lifecycle["upgradeVersion"]
        assert tuple(map(int, upgrade["version"].split("."))) > tuple(
            map(int, base["version"].split("."))
        )
        assert base["productCode"] != upgrade["productCode"]
        assert smoke["suite"] == "windows-msi-package-smoke"
        assert smoke["outcome"] == "privileged-passed"
        assert smoke["imageVariant"] == "package-clean"
        assert all(smoke["preconditions"].values())
        assert smoke["service"]["state"] == "Running"
        assert all(prepare["stateCreated"].values())
        assert prepare["serviceRestart"]["stopped"]
        assert prepare["serviceRestart"]["processIdBefore"] != prepare["serviceRestart"]["processIdAfter"]
        assert hashlib.sha256(base_msi).hexdigest() == base["sha256"]
        assert hashlib.sha256(upgrade_msi).hexdigest() == upgrade["sha256"]
        for phase in ("reboot", "failureRecovery", "repair", "upgrade",
                      "uninstall", "reinstall"):
            assert lifecycle[phase]["passed"]
        assert lifecycle["failureRecovery"]["processIdBefore"] != lifecycle["failureRecovery"]["processIdAfter"]
        assert lifecycle["repair"]["aclReconciled"]
        assert lifecycle["upgrade"]["processIdBefore"] != lifecycle["upgrade"]["processIdAfter"]
        assert lifecycle["downgrade"]["rejected"]
        assert lifecycle["downgrade"]["exitCode"] not in (0, 3010)
        assert lifecycle["uninstall"]["programDataRetained"]
        assert lifecycle["uninstall"]["driverRemoved"]
        assert lifecycle["featureSelection"]["passed"]
        assert lifecycle["featureSelection"]["appRetained"]
        assert lifecycle["featureSelection"]["autoStartRemoved"]
        assert lifecycle["reinstall"]["retainedStateUsable"]
        assert lifecycle["reinstall"]["serviceOnly"]
        assert all(lifecycle["retained"].values())

        args.output.mkdir(parents=True, exist_ok=True)
        for member in members:
            if member.isfile():
                destination = args.output / PurePosixPath(member.name).name
                destination.write_bytes(archive.extractfile(member).read())


if __name__ == "__main__":
    main()
