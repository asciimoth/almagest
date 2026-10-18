#!/usr/bin/env python3
"""Run all Go fuzz targets with one shared fuzzing-time budget."""

import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys


ROOT = Path(__file__).resolve().parents[1]
GO_MODULES = ("certcat", "lib", "bin")
FUZZ_NAME = re.compile(r"Fuzz[A-Za-z0-9_]+")
FUZZ_DEADLINE_FAILURE = re.compile(
    r"(?m)^--- FAIL: Fuzz[A-Za-z0-9_]+ \([^)]+\)$\n"
    r"^[ \t]+context deadline exceeded$\n"
    r"^FAIL$\n"
    r"(?:^exit status 1$\n)?"
    r"^FAIL[ \t]+\S+[ \t]+\S+[ \t]*(?:\n)?\Z"
)
GO_DURATION_PART = re.compile(r"(\d+(?:\.\d+)?)(ns|us|µs|μs|ms|s|m|h)")
UNIT_NANOSECONDS = {
    "ns": 1,
    "us": 1_000,
    "µs": 1_000,
    "μs": 1_000,
    "ms": 1_000_000,
    "s": 1_000_000_000,
    "m": 60_000_000_000,
    "h": 3_600_000_000_000,
}


def parse_duration(value):
    """Parse the duration syntax accepted by Go's time.ParseDuration."""
    position = 0
    nanoseconds = 0.0
    for match in GO_DURATION_PART.finditer(value):
        if match.start() != position:
            raise ValueError(f"invalid Go duration: {value!r}")
        nanoseconds += float(match.group(1)) * UNIT_NANOSECONDS[match.group(2)]
        position = match.end()
    if position != len(value) or position == 0 or nanoseconds <= 0:
        raise ValueError(f"invalid positive Go duration: {value!r}")
    return int(nanoseconds)


def run(command, *, cwd, capture=False):
    print("==> fuzz:", shlex.join(command), flush=True)
    return subprocess.run(
        command,
        cwd=cwd,
        check=False,
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.STDOUT if capture else None,
        text=True,
    )


def run_streaming(command, *, cwd):
    """Run a command while retaining output needed for failure classification."""
    print("==> fuzz:", shlex.join(command), flush=True)
    output = []
    with subprocess.Popen(
        command,
        cwd=cwd,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    ) as process:
        for line in process.stdout:
            print(line, end="", flush=True)
            output.append(line)
        returncode = process.wait()
    return subprocess.CompletedProcess(command, returncode, "".join(output))


def is_fuzz_deadline_failure(output):
    """Return true for Go's infrastructure-only fuzz deadline failure."""
    return FUZZ_DEADLINE_FAILURE.search(output) is not None


def discover_targets(module):
    command = [
        "go", "test", "-json", "-run=^$", "-list=^Fuzz", "./...",
    ]
    result = run(command, cwd=ROOT / module, capture=True)
    targets = []
    for line in result.stdout.splitlines():
        try:
            event = json.loads(line)
        except (ValueError, UnicodeError):
            continue
        name = event.get("Output", "").strip()
        package = event.get("Package")
        if package and FUZZ_NAME.fullmatch(name):
            targets.append((module, package, name))
    if result.returncode:
        print(result.stdout, end="" if result.stdout.endswith("\n") else "\n")
        raise RuntimeError(f"could not discover fuzz targets in {module}")
    return targets


def main():
    try:
        total_nanoseconds = parse_duration(os.environ.get("FUZZ_TIME", "60s"))
    except ValueError as error:
        print(f"FUZZ_TIME: {error}", file=sys.stderr)
        return 2

    try:
        targets = sorted(
            target
            for module in GO_MODULES
            for target in discover_targets(module)
        )
    except RuntimeError as error:
        print(error, file=sys.stderr)
        return 1

    if not targets:
        print("No Go fuzz targets found.")
        return 0
    if total_nanoseconds < len(targets):
        print(
            f"FUZZ_TIME must be at least {len(targets)}ns for "
            f"{len(targets)} fuzz targets.",
            file=sys.stderr,
        )
        return 2

    base, remainder = divmod(total_nanoseconds, len(targets))
    print(
        f"Running {len(targets)} fuzz targets with shared budget "
        f"{os.environ.get('FUZZ_TIME', '60s')}.",
        flush=True,
    )
    failed = False
    for index, (module, package, name) in enumerate(targets):
        target_nanoseconds = base + (1 if index < remainder else 0)
        result = run_streaming(
            [
                "go", "test", package, "-run=^$", f"-fuzz=^{name}$",
                f"-fuzztime={target_nanoseconds}ns",
            ],
            cwd=ROOT / module,
        )
        if result.returncode == 0:
            continue
        if is_fuzz_deadline_failure(result.stdout):
            print(
                f"WARNING: {package}.{name} exceeded its fuzz deadline "
                "during coordinator shutdown; no failing input was reported.",
                file=sys.stderr,
                flush=True,
            )
            continue
        failed = True
    return int(failed)


if __name__ == "__main__":
    sys.exit(main())
