#!/usr/bin/env python3
"""Run an explicit narrow set of project tests with their required setup."""

import argparse
from dataclasses import dataclass
import json
import os
from pathlib import Path
import platform
import re
import shlex
import signal
import subprocess
import sys


ROOT = Path(__file__).resolve().parents[1]
GO_MODULES = {"certcat", "lib", "bin"}
WINVM_MANIFEST = ROOT / "dev/winvm/test-suites.json"
TEST_NAME = re.compile(r"(?:Test|Fuzz|Example)[A-Za-z0-9_]*(?:/[A-Za-z0-9_.-]+)*$")


class SelectionError(ValueError):
    """A selector is invalid or does not identify a test."""


@dataclass(frozen=True)
class GoSelection:
    module: str
    package: str
    test: str


@dataclass
class Selections:
    go: list[GoSelection]
    e2e: list[str]
    winvm: list[str]
    javascript: list[str]
    python: list[str]
    windows_package: list[str]


def split_arguments(arguments):
    """Restore the variadic arguments joined by just's recipe interpolation."""
    values = []
    for argument in arguments:
        try:
            values.extend(shlex.split(argument))
        except ValueError as error:
            raise SelectionError(f"cannot parse selectors: {error}") from error
    return values


def exact_go_pattern(name):
    return "/".join(f"^{re.escape(part)}$" for part in name.split("/"))


def parse_go(value, root):
    try:
        package_path, test = value.rsplit(":", 1)
    except ValueError as error:
        raise SelectionError(
            "Go selectors must use go:MODULE[/PACKAGE]:TEST"
        ) from error
    parts = package_path.split("/")
    module = parts[0]
    if module not in GO_MODULES:
        raise SelectionError(f"unknown Go module in selector: {module}")
    if not TEST_NAME.fullmatch(test):
        raise SelectionError(f"invalid exact Go test name: {test}")
    relative = Path(*parts[1:]) if len(parts) > 1 else Path(".")
    if any(part in ("", ".", "..") for part in parts[1:]):
        raise SelectionError(f"invalid Go package path: {package_path}")
    package_dir = (root / module / relative).resolve()
    module_dir = (root / module).resolve()
    if not package_dir.is_relative_to(module_dir) or not package_dir.is_dir():
        raise SelectionError(f"Go package directory does not exist: {package_path}")
    package = "." if relative == Path(".") else "./" + relative.as_posix()
    return GoSelection(module, package, test)


def load_winvm_cases(path=WINVM_MANIFEST):
    manifest = json.loads(path.read_text())
    cases = {}
    for suite in manifest["suites"]:
        for test_case in suite["cases"]:
            if test_case["kind"] == "go":
                cases[test_case["id"]] = suite["privilege"]
    return cases


def parse_selectors(values, root=ROOT, manifest_path=WINVM_MANIFEST):
    if not values:
        raise SelectionError("at least one test selector is required")
    selected = Selections([], [], [], [], [], [])
    winvm_cases = load_winvm_cases(manifest_path)
    seen = set()
    for selector in values:
        if selector in seen:
            continue
        seen.add(selector)
        if selector.startswith("go:"):
            selected.go.append(parse_go(selector.removeprefix("go:"), root))
        elif selector.startswith("e2e:"):
            name = selector.removeprefix("e2e:")
            if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]*", name):
                raise SelectionError(f"invalid E2E case name: {name}")
            if not (root / "e2e/cases" / name).is_dir():
                raise SelectionError(f"E2E case does not exist: {name}")
            selected.e2e.append(name)
        elif selector.startswith("winvm:"):
            case_id = selector.removeprefix("winvm:")
            if case_id not in winvm_cases:
                raise SelectionError(f"WinVM Go case does not exist: {case_id}")
            selected.winvm.append(case_id)
        elif selector.startswith("js:"):
            name = selector.removeprefix("js:")
            relative = Path(name.removeprefix("panel/"))
            path = (root / "panel" / relative).resolve()
            tests = (root / "panel/tests").resolve()
            if (not path.is_relative_to(tests) or path.suffix != ".mjs"
                    or not path.is_file()):
                raise SelectionError(f"panel JavaScript test does not exist: {name}")
            selected.javascript.append(path.relative_to(root / "panel").as_posix())
        elif selector.startswith("python:"):
            name = selector.removeprefix("python:")
            if not re.fullmatch(r"(?:scripts\.tests|dev\.winvm\.tests)\.test_[A-Za-z0-9_.]+", name):
                raise SelectionError(f"invalid Python unittest name: {name}")
            module = name.split(".")
            file_path = root.joinpath(*module[:3]).with_suffix(".py")
            if module[:3] == ["dev", "winvm", "tests"]:
                file_path = root.joinpath(*module[:4]).with_suffix(".py")
            if not file_path.is_file():
                raise SelectionError(f"Python unittest module does not exist: {name}")
            selected.python.append(name)
        elif selector.startswith("winpkg:"):
            name = selector.removeprefix("winpkg:")
            if name not in {"build", "smoke", "lifecycle", "integration"}:
                raise SelectionError(f"unknown Windows package selector: {name}")
            selected.windows_package.append(name)
        else:
            raise SelectionError(
                f"unknown selector {selector!r}; use go:, e2e:, winvm:, js:, "
                "python:, or winpkg:"
            )
    return selected, winvm_cases


class Harness:
    def __init__(self, root=ROOT, run=subprocess.run):
        self.root = root
        self.run_process = run
        self.failed = False

    def command(self, argv, *, env=None, cwd=None):
        print("==> selected:", shlex.join(str(value) for value in argv), flush=True)
        result = self.run_process(
            [str(value) for value in argv], cwd=cwd or self.root, env=env, check=False
        )
        if result.returncode:
            self.failed = True
            return False
        return True

    def winvm_command(self, argv, *, env):
        if self.run_process is not subprocess.run:
            return self.command(argv, env=env)
        print("==> selected:", shlex.join(str(value) for value in argv), flush=True)
        process = subprocess.Popen(
            [str(value) for value in argv], cwd=self.root, env=env,
            start_new_session=True,
        )
        interrupted = False
        while True:
            try:
                returncode = process.wait()
                break
            except KeyboardInterrupt:
                if not interrupted:
                    # The VM supervisor owns QEMU cleanup. Give it the first
                    # interrupt and wait for it instead of killing the
                    # supervisor during its bounded cleanup sequence.
                    os.killpg(process.pid, signal.SIGINT)
                    interrupted = True
                    print("Waiting for the WinVM supervisor to reap QEMU...", flush=True)
        if returncode:
            self.failed = True
            return False
        return True

    def prepare_host_build(self, selected):
        if ((selected.go or selected.e2e)
                and not self.command(["just", "repair-go-work"])):
            return False
        if selected.go and not self.command(["just", "certcat-ensure-generated"]):
            return False
        if ((selected.go or selected.e2e or selected.javascript)
                and not self.command(["just", "build-web"])):
            return False
        return True

    def go_test(self, test):
        fuzz = test.test.startswith("Fuzz") and "/" not in test.test
        command = ["go", "-C", test.module, "test", test.package]
        if fuzz:
            command.extend([
                "-run=^$", f"-fuzz={exact_go_pattern(test.test)}",
                f"-fuzztime={os.environ.get('FUZZ_TIME', '5m')}", "-json",
            ])
        else:
            command.extend([
                "-run", exact_go_pattern(test.test), "-count=1", "-json",
            ])
        if test.module != "certcat" and not fuzz:
            command.append("-race")
        print("==> selected:", shlex.join(command), flush=True)
        result = self.run_process(
            command, cwd=self.root, check=False, stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT, text=True,
        )
        if result.stdout:
            print(result.stdout, end="" if result.stdout.endswith("\n") else "\n")
        ran = False
        for line in (result.stdout or "").splitlines():
            try:
                event = json.loads(line)
            except (ValueError, UnicodeError):
                continue
            if event.get("Action") == "run" and event.get("Test") == test.test:
                ran = True
        if result.returncode or not ran:
            if not ran:
                print(f"selected Go test did not run: {test.test}", file=sys.stderr)
            self.failed = True

    def execute(self, selected, winvm_cases):
        if not self.prepare_host_build(selected):
            return 1

        if selected.go:
            for test in selected.go:
                self.go_test(test)

        if selected.javascript:
            self.command(["node", "--test", *selected.javascript], cwd=self.root / "panel")

        for test in selected.python:
            self.command([sys.executable, "-m", "unittest", test])

        if selected.e2e:
            environment = dict(os.environ, E2E_COVERAGE="0")
            self.command(["./e2e/run.sh", *selected.e2e], env=environment)

        if selected.winvm:
            environment = dict(
                os.environ,
                WINVM_TEST_CASES=",".join(selected.winvm),
                WINVM_COVERAGE_DIR=str(self.root / "coverage/selected/winvm"),
            )
            matrix_cases = {
                "sandbox::windows-mode-matrix-ordinary",
                "sandbox::windows-mode-matrix-elevated",
            }
            if set(selected.winvm) <= matrix_cases:
                environment["WINVM_MEMORY_MIB"] = "4096"
            privileges = {winvm_cases[case_id] for case_id in selected.winvm}
            if platform.system() == "Windows":
                for privilege in ("ordinary", "privileged"):
                    if privilege in privileges:
                        self.command(["./scripts/test-windows.sh", privilege], env=environment)
            else:
                if self.command(["./dev/winvm/ensure-image.sh"]):
                    mode = next(iter(privileges)) if len(privileges) == 1 else "test"
                    self.winvm_command(["./dev/winvm/run.sh", mode], env=environment)

        if selected.windows_package:
            version = os.environ.get("ALMAGEST_PACKAGE_VERSION", "0.0.0")
            recipes = {
                "build": "package-windows",
                "smoke": "winvm-package-smoke",
                "lifecycle": "winvm-package-lifecycle",
                "integration": "winvm-package-integration",
            }
            for name in selected.windows_package:
                self.winvm_command(["just", recipes[name], version], env=os.environ.copy())

        return 1 if self.failed else 0


def main(arguments=None):
    parser = argparse.ArgumentParser(
        description=__doc__,
        epilog=("Selectors: go:MODULE[/PACKAGE]:TEST, e2e:CASE, winvm:CASE-ID, "
                "js:panel/tests/FILE.mjs, python:DOTTED-UNITTEST, "
                "winpkg:{build,smoke,lifecycle,integration}"),
    )
    parser.add_argument("selectors", nargs="+")
    args = parser.parse_args(arguments)
    try:
        values = split_arguments(args.selectors)
        selected, winvm_cases = parse_selectors(values)
    except (OSError, ValueError, KeyError, json.JSONDecodeError) as error:
        parser.error(str(error))
    return Harness().execute(selected, winvm_cases)


if __name__ == "__main__":
    raise SystemExit(main())
