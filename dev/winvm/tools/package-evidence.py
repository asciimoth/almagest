#!/usr/bin/env python3
"""Validate and extract a WinVM package build artifact."""

import argparse
import hashlib
import json
import tarfile
from pathlib import Path


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("archive", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    with tarfile.open(args.archive) as archive:
        evidence = json.load(archive.extractfile("artifacts/package-build.json"))
        assert evidence["schemaVersion"] == 1
        assert evidence["suite"] == "windows-msi-build"
        assert evidence["outcome"] == "passed"
        name = evidence["package"]
        assert Path(name).name == name and name.endswith("_windows_amd64.msi")
        msi = archive.extractfile(f"artifacts/{name}").read()
        checksum = archive.extractfile(f"artifacts/{name}.sha256").read().decode()
    digest = hashlib.sha256(msi).hexdigest()
    assert digest == evidence["sha256"]
    assert checksum == f"{digest}  {name}\r\n" or checksum == f"{digest}  {name}\n"
    args.output.mkdir(parents=True, exist_ok=True)
    (args.output / name).write_bytes(msi)
    (args.output / f"{name}.sha256").write_text(f"{digest}  {name}\n")
    (args.output / "package-build.json").write_text(json.dumps(evidence, indent=2) + "\n")


if __name__ == "__main__":
    main()
