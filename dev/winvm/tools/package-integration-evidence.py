#!/usr/bin/env python3
"""Validate and extract WinVM MSI package integration evidence."""

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
        build = json.load(archive.extractfile("artifacts/package-build.json"))
        smoke = json.load(archive.extractfile("artifacts/package-smoke.json"))
        user = json.load(archive.extractfile("artifacts/package-user-smoke.json"))
        integration = json.load(
            archive.extractfile("artifacts/package-integration.json"))
        msi = archive.extractfile(f"artifacts/{build['package']}").read()

        assert smoke["schemaVersion"] == user["schemaVersion"] == 1
        assert integration["schemaVersion"] == 1
        assert smoke["suite"] == "windows-msi-package-smoke"
        assert smoke["outcome"] == "privileged-passed"
        assert smoke["imageVariant"] == "package-clean"
        assert all(smoke["preconditions"].values())
        assert smoke["service"]["state"] == "Running"
        assert smoke["elevatedCli"] == "passed"
        assert smoke["administrationGroup"]["initialMemberCount"] == 1
        assert smoke["administrationGroup"]["pipeAccess"] == "passed"
        assert user["suite"] == "windows-msi-package-smoke-user"
        assert user["outcome"] == "passed"
        assert user["programFilesWriteDenied"]
        assert user["machinePipeAccessGranted"]
        assert user["protectedConfigReadDenied"]

        assert integration["suite"] == "windows-msi-package-integration"
        assert integration["outcome"] == "passed"
        assert integration["installedService"] == "almagestd"
        assert integration["installedCli"].lower().endswith(
            r"\program files\almagest\almagest-cli.exe")
        assert set(integration["apiSurfaces"]) == {
            "admin", "config", "dns", "docs", "engine", "kv", "log",
            "secrets", "snapshots", "tls",
        }
        for key in ("localizedDocumentation", "packagedExamples",
                    "configAppliedToProgramData", "localProxyOutNetwork"):
            assert integration[key]
        assert all(integration["scmRestart"].values())
        assert integration["example"]["id"]
        assert integration["example"]["loaded"]
        assert integration["example"]["retainedAfterRestart"]
        assert integration["nativeTun"]["safePrivateRoute"]
        adapter = integration["nativeTun"]["adapter"]
        assert adapter["name"].lower().startswith("almpkg")
        assert adapter["signed"]
        assert "wireguard" in adapter["driverProvider"].lower()
        assert adapter["address"] == "198.18.239.1/32"
        assert adapter["route"] == "198.19.239.0/24"
        assert all(integration["normalServiceStop"].values())
        assert hashlib.sha256(msi).hexdigest() == build["sha256"]
        assert smoke["package"] == build["package"]

        args.output.mkdir(parents=True, exist_ok=True)
        for member in members:
            if member.isfile():
                destination = args.output / PurePosixPath(member.name).name
                destination.write_bytes(archive.extractfile(member).read())


if __name__ == "__main__":
    main()
