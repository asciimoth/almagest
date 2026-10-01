#!/usr/bin/env python3
"""Combine Windows VM Go coverage profiles without extracting the archive."""

import os
from pathlib import Path
import sys
import tarfile
import tempfile


def combine(archive_path, output_path):
    counts = {}
    statements = {}
    profiles = 0
    mode = None
    with tarfile.open(archive_path) as archive:
        for member in archive.getmembers():
            name = member.name.replace("\\", "/")
            if not (
                member.isfile()
                and name.startswith("artifacts/")
                and "/coverage/" in name
                and name.endswith(".out")
            ):
                continue
            profiles += 1
            lines = archive.extractfile(member).read().decode("utf-8-sig").splitlines()
            if not lines or not lines[0].startswith("mode: "):
                raise RuntimeError(f"invalid coverage profile: {name}")
            profile_mode = lines[0].removeprefix("mode: ")
            if mode is None:
                mode = profile_mode
            elif mode != profile_mode:
                raise RuntimeError(f"coverage mode mismatch in {name}")
            for number, line in enumerate(lines[1:], 2):
                if not line.strip():
                    continue
                try:
                    location, statement_count, execution_count = line.rsplit(None, 2)
                    statement_count = int(statement_count)
                    execution_count = int(execution_count)
                except ValueError as error:
                    raise RuntimeError(
                        f"invalid coverage record in {name}:{number}"
                    ) from error
                if not location.startswith("github.com/asciimoth/almagest/"):
                    continue
                if location in statements and statements[location] != statement_count:
                    raise RuntimeError(f"statement count mismatch for {location}")
                statements[location] = statement_count
                counts[location] = counts.get(location, 0) + execution_count

    if profiles == 0:
        raise RuntimeError("native test coverage profiles are absent")

    output = Path(output_path)
    output.mkdir(parents=True, exist_ok=True)
    covered = sum(value for location, value in statements.items() if counts[location] > 0)
    total = sum(statements.values())
    percent = 100 * covered / total if total else 0
    files = {
        "coverage.out": "".join(
            [f"mode: {mode}\n"]
            + [
                f"{location} {statements[location]} {counts[location]}\n"
                for location in sorted(statements)
            ]
        ),
        "total.txt": f"total:\t(statements)\t{percent:.1f}%\n",
    }
    for name, content in files.items():
        descriptor, temporary = tempfile.mkstemp(prefix=f".{name}.", dir=output)
        try:
            with os.fdopen(descriptor, "w", encoding="utf-8", newline="") as stream:
                stream.write(content)
            os.replace(temporary, output / name)
        except BaseException:
            os.unlink(temporary)
            raise


if __name__ == "__main__":
    if len(sys.argv) != 3:
        raise SystemExit(f"usage: {sys.argv[0]} ARTIFACT OUTPUT_DIRECTORY")
    combine(sys.argv[1], sys.argv[2])
