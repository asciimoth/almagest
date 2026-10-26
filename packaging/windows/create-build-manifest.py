#!/usr/bin/env python3
"""Create deterministic identities and an input manifest for one MSI build."""

import argparse
import hashlib
import json
import re
import uuid
from pathlib import Path


def sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True)
    parser.add_argument("--root", required=True, type=Path)
    parser.add_argument("--source-record", required=True, type=Path)
    parser.add_argument("--image-lock", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()

    match = re.fullmatch(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", args.version)
    if not match or any(value > limit for value, limit in
                        zip(map(int, match.groups()), (255, 255, 65535))):
        parser.error("version must be an MSI x.y.z version")

    contract = json.loads((args.root / "packaging/windows/contract.json").read_text())
    source = json.loads(args.source_record.read_text())
    included = []
    paths = [
        args.root / "dev/winvm/almagestd.exe",
        args.root / "dev/winvm/almagest-cli.exe",
        args.root / "dev/winvm/almagest-app.exe",
        args.root / "LICENSE",
        args.root / "packaging/windows/README.md",
    ]
    for directory in ("locales", "examples", "dev/winvm/package-inputs"):
        paths.extend(path for path in (args.root / directory).rglob("*") if path.is_file())
    for path in sorted(path for path in paths if path.is_file()):
        included.append({
            "path": path.relative_to(args.root).as_posix(),
            "sha256": sha256(path),
        })
    inputs = {
        "sourceArchiveSHA256": source["sha256"],
        "imageLockSHA256": sha256(args.image_lock),
        "files": included,
    }
    canonical = json.dumps(inputs, sort_keys=True, separators=(",", ":"))
    identity = contract["identity"]
    product_code = uuid.uuid5(uuid.UUID(identity["productCodeNamespace"]), args.version)
    package_code = uuid.uuid5(
        uuid.UUID(identity["packageCodeNamespace"]), f"{args.version}\n{canonical}"
    )
    manifest = {
        "schemaVersion": 1,
        "version": args.version,
        "architecture": "amd64",
        "output": f"almagest_{args.version}_windows_amd64.msi",
        "productCode": str(product_code).upper(),
        "packageCode": str(package_code).upper(),
        "source": {
            "revision": source["revision"],
            "dirty": source["dirty"],
            "archiveSHA256": source["sha256"],
        },
        "inputs": inputs,
        "reproducibility": {
            "identityPolicy": "Version and complete hashed MSI inputs determine package identity.",
            "expectedOutput": "byte-for-byte identical for identical manifest inputs",
        },
    }
    args.output.write_text(json.dumps(manifest, indent=2) + "\n")


if __name__ == "__main__":
    main()
