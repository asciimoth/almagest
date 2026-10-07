#!/usr/bin/env python3
"""Run independent Go, Docker, and Windows VM test loops until signalled."""

import argparse
import collections
import datetime
import json
import os
from pathlib import Path
import platform
import re
import select
import shlex
import shutil
import signal
import subprocess
import tarfile
import tempfile
import threading
import time
import traceback

ROOT = Path(__file__).resolve().parents[1]
DEFAULTS = {
    "E2E_JOBS": "4", "E2E_INCLUDE_PRIVILEGED": "0",
    "E2E_EXCLUDE_HEAVY": "0", "E2E_COVERAGE": "0",
    "E2E_IMAGE": "almagest-e2e:local", "E2E_REBUILD": "0",
    "E2E_FAIL_FAST": "0",
    "WINVM_FAIL_FAST": "0",
    "RUN_WINVM_TESTS": "0",
}
GO_FLAGS = ["-count=1", "-p=1", "-parallel=1", "-json"]


def go_commands():
    commands = [
        ["go", "-C", module, "test", *GO_FLAGS,
         *(["-race"] if module != "certcat" else []), "./..."]
        for module in ("certcat", "lib", "bin")
    ]
    for package, pattern in (
        ("spoofer", "^TestSetupTCPOptionsDisablesTimeWait$"),
        ("vtun", "^TestSpoofer(AcceptsReusedClientTupleAfterServerClose|"
         "RepeatedlyReusesClientTuple|ReusesManyClientTuplesConcurrently|"
         "ReusesClientTupleAfterReset|ShutdownWhileReusedTupleIsBeingPrepared)$"),
    ):
        commands.append(["go", "-C", "lib", "test", *GO_FLAGS,
                         f"github.com/asciimoth/gonnect-netstack/{package}",
                         "-run", pattern])
    return commands


def go_failures(lines):
    tests, packages = set(), set()
    for line in lines:
        try:
            event = json.loads(line)
        except (ValueError, UnicodeError):
            continue
        if not isinstance(event, dict):
            continue
        # Go can emit only a package-level fail after its timeout panic. The
        # panic output still identifies the active test in a serialized run.
        timeout = (event.get("Action") == "output" and event.get("Test")
                   and event.get("Output", "").startswith("panic: test timed out after"))
        if event.get("Action") != "fail" and not timeout:
            continue
        package = event.get("Package", "unknown package")
        if event.get("Test"):
            tests.add((package, event["Test"]))
        else:
            packages.add(package)
    # Count leaf subtests once, without also counting their failed parents.
    names = {f"{pkg}/{test}" for pkg, test in tests
             if not any(p == pkg and t.startswith(test + "/") for p, t in tests)}
    names.update(f"{pkg} (package failure)" for pkg in packages
                 if not any(p == pkg for p, _ in tests))
    return names


def failure_names(kind, directory, vm_dir=None):
    names = set()
    for log in directory.glob("command-*.log"):
        with log.open(errors="replace") as stream:
            if kind == "go":
                names.update(go_failures(stream))
            else:
                for line in stream:
                    match = re.search(r"==> e2e: FAIL (\S+)", line)
                    if match:
                        names.add(match[1])
    if kind == "winvm" and vm_dir:
        for filename in ("guest-artifacts.tar", "guest-artifacts.snapshot.tar"):
            archive = vm_dir / filename
            if not archive.exists():
                continue
            try:
                with tarfile.open(archive) as bundle:
                    for member in bundle:
                        if "/events/" in member.name and member.isfile() and not member.name.endswith(".log"):
                            with bundle.extractfile(member) as stream:
                                names.update(go_failures(line.decode("utf-8-sig", errors="replace") for line in stream))
            except (OSError, tarfile.TarError):
                pass  # The command log still records an artifact collection failure.
        if not names:
            for filename in ("ordinary-tests.log", "privileged-tests.log", "privileged-progress.log"):
                path = vm_dir / filename
                if path.exists():
                    for line in path.read_text(errors="replace").splitlines():
                        match = re.search(r"\bFAIL\s+(\S+)", line)
                        if match:
                            names.add(match[1] + " (suite case)")
        if not names and (vm_dir / "run.json").exists():
            try:
                reason = json.loads((vm_dir / "run.json").read_text()).get("failureKind")
                if reason:
                    names.add(f"winvm/{reason} (infrastructure failure)")
            except (OSError, ValueError):
                pass
    return sorted(names)


class Runner:
    def __init__(self, root=ROOT, env=None):
        self.root = root
        self.env = dict(os.environ if env is None else env)
        for key, value in DEFAULTS.items():
            if not self.env.get(key):
                self.env[key] = value
        self.stop = threading.Event()
        self.lock = threading.Lock()
        self.reason = "running"
        self.errors = []
        self.started = datetime.datetime.now(datetime.timezone.utc).isoformat()
        parent = root / "artifacts/test-cycles"
        parent.mkdir(parents=True, exist_ok=True)
        self.session = Path(tempfile.mkdtemp(prefix="run-", dir=parent))
        self.stats = {kind: dict(started=0, completed=0, failed=0, interrupted=0,
                                 successful_seconds=0.0, failures=collections.Counter())
                      for kind in ("go", "e2e", "winvm")}
        self.enabled = ["go", "e2e"]
        if self.env["RUN_WINVM_TESTS"] == "1":
            self.enabled.append("winvm")
        self.flags = {key: self.env[key] for key in DEFAULTS}
        for key in ("E2E_RANDOM_SEED", "E2E_BUILD_ATTEMPTS", "E2E_BUILD_RETRY_DELAY",
                    "E2E_BUILD_RETRY_MAX_DELAY", "E2E_COVERPKG", "GOFLAGS",
                    "GOMAXPROCS", "WINVM_ENV_FILE", "WINVM_CACHE_DIR"):
            if key in self.env:
                self.flags[key] = self.env[key]
        self.context = {"started_utc": self.started, "host": platform.platform(),
                        "root": str(root), "flags": self.flags, "go_flags": GO_FLAGS}

    def say(self, message):
        with self.lock:
            print(message, flush=True)

    def signal(self, number, _frame):
        self.reason = signal.Signals(number).name
        self.stop.set()

    def command(self, argv, log, env=None):
        """Give each command a process group so stop reaches all its children."""
        if self.stop.is_set():
            return 130
        with log.open("ab") as output:
            output.write(("$ " + shlex.join(argv) + "\n").encode())
            output.flush()
            try:
                process = subprocess.Popen(argv, cwd=self.root, env=env or self.env,
                                           stdout=output, stderr=subprocess.STDOUT,
                                           start_new_session=True)
            except OSError as error:
                output.write((str(error) + "\n").encode())
                return 127
            deadline = None
            vm_supervisor = argv[:2] in (["./dev/winvm/run.sh", "test"],
                                         ["./dev/winvm/run.sh", "smoke"]) or \
                argv[:1] == ["./dev/winvm/flow-run.sh"]
            cleanup_seconds = 60
            if vm_supervisor:
                config = self.root / "dev/winvm/config.json"
                machine = json.loads(config.read_text())["machine"] if config.exists() else {}
                # The supervisor owns QEMU, diagnostics, and one evidence-only
                # recovery boot. Do not kill it halfway through that budget.
                cleanup_seconds = machine.get("cleanupTimeoutSeconds", 300) + 15
            while process.poll() is None:
                if self.stop.is_set() and deadline is None:
                    if vm_supervisor or argv[:2] == ["./e2e/run.sh", "--skip-build"]:
                        # The scheduler removes containers, then signals and
                        # waits for case runners. A group signal here would
                        # race their network cleanup with that second signal.
                        process.terminate()
                    else:
                        self.kill_group(process, signal.SIGTERM)
                    deadline = time.monotonic() + cleanup_seconds
                if deadline is not None and time.monotonic() >= deadline:
                    self.kill_group(process, signal.SIGKILL)
                    if vm_supervisor:
                        self.stop_vm_after_supervisor_timeout((env or self.env).get("WINVM_RUN_DIR"))
                    self.errors.append(f"Cleanup timed out: {shlex.join(argv)}; inspect {log}")
                    break
                time.sleep(0.1)
            status = process.wait()
            if deadline is not None:
                # Reap any children that outlived their shell's cleanup handler.
                self.kill_group(process, signal.SIGKILL)
            return status

    def stop_vm_after_supervisor_timeout(self, directory):
        """Last resort if the supervisor itself hangs; never signal a reused PID."""
        if not directory:
            return
        run = Path(directory).resolve()
        parent = (self.root / "artifacts/winvm").resolve()
        if run.parent != parent or not run.name.startswith("run-"):
            self.errors.append("Refused VM fallback outside the artifact root")
            return
        try:
            state = json.loads((run / "supervisor-state.json").read_text())
            pid = int(state["qemuPid"])
            stat_path = Path(f"/proc/{pid}/stat")
            stat = stat_path.read_text().rsplit(")", 1)[1].split()
            if stat[19] != str(state["qemuStartTime"]):
                raise ValueError("QEMU PID was reused")
            # pidfd pins the checked process across the final signal. Recheck
            # after opening it so PID reuse cannot target another process.
            fd = os.pidfd_open(pid)
            try:
                again = stat_path.read_text().rsplit(")", 1)[1].split()
                if again[19] != stat[19]:
                    raise ValueError("QEMU PID changed during fallback")
                (run / "supervisor-timeout.log").write_text(
                    "Supervisor cleanup exceeded its host budget. QEMU SIGKILL requested; "
                    "retained disk and last host archive may contain incomplete logs.\n" +
                    Path(f"/proc/{pid}/status").read_text())
                signal.pidfd_send_signal(fd, signal.SIGKILL)
                exited = select.poll()
                exited.register(fd, select.POLLIN)
                if not exited.poll(3000):
                    marker = Path(state["orphanMarker"])
                    if marker.name != "qemu-orphan" or marker.parent.name != "locks":
                        raise ValueError("invalid QEMU orphan marker path")
                    marker.write_text(f"{pid} {stat[19]}\n")
                    self.errors.append(f"QEMU {pid} survived SIGKILL; subsequent VM runs are blocked")
            finally:
                os.close(fd)
        except (OSError, ValueError, KeyError) as error:
            self.errors.append(f"VM fallback: {error}; inspect {run}")
        path = run / "run.json"
        if path.exists():
            record = json.loads(path.read_text())
            record.update(status="failed", failureKind="supervisor-cleanup-timeout",
                          exitStatus=124, finalArtifactsCollected=False)
            temporary = path.with_suffix(".json.new")
            temporary.write_text(json.dumps(record, indent=2) + "\n")
            temporary.replace(path)

    @staticmethod
    def kill_group(process, number):
        try:
            os.killpg(process.pid, number)
        except ProcessLookupError:
            pass

    def prepare(self):
        self.say(f"Preparation starting; logs: {self.session}")
        commands = [
            ["git", "rev-parse", "HEAD"], ["git", "status", "--short"],
            ["go", "version"], ["docker", "version"],
            ["just", "certcat-ensure-generated"], ["just", "build-web"],
            ["./e2e/run.sh", "--prepare-only"],
        ]
        if "winvm" in self.enabled:
            prepared = self.session / "winvm-prepared"
            prepared.mkdir()
            commands += [["./dev/winvm/build-image.sh"],
                         ["./dev/winvm/run.sh", "--prepare", str(prepared)]]
        for argv in commands:
            if self.command(argv, self.session / "preparation.log") != 0:
                if not self.stop.is_set():
                    self.reason = "preparation failed"
                    self.errors.append(f"Preparation failed: {shlex.join(argv)}")
                return False
        # Keep the source identity in the copyable report, not only in a log.
        self.context["revision"] = subprocess.check_output(
            ["git", "rev-parse", "HEAD"], cwd=self.root, text=True).strip()
        self.context["worktree"] = subprocess.check_output(
            ["git", "status", "--short"], cwd=self.root, text=True).strip() or "clean"
        (self.session / "context.json").write_text(json.dumps(self.context, indent=2) + "\n")
        self.say("Preparation finished; starting independent test loops")
        return True

    def loop(self, kind):
        try:
            while not self.stop.is_set():
                self.iteration(kind)
                # Avoid a hot retry loop if a tool becomes unavailable.
                self.stop.wait(0.2)
        except Exception:
            details = traceback.format_exc()
            (self.session / f"{kind}-runner-error.log").write_text(details)
            self.errors.append(f"{kind} runner error; see {kind}-runner-error.log")
            self.reason = "runner error"
            self.stop.set()

    def iteration(self, kind):
        stats = self.stats[kind]
        stats["started"] += 1
        index = stats["started"]
        directory = self.session / kind / f"loop-{index:06d}"
        directory.mkdir(parents=True)
        env = self.env.copy()
        vm_dir = None
        flow_dir = None
        if kind == "go":
            commands = go_commands()
            # Inherited GOFLAGS must not override the explicit test settings.
        elif kind == "e2e":
            commands = [["./e2e/run.sh", "--skip-build"]]
            env["E2E_FAIL_FAST"] = "0"
            env["E2E_LOG_DIR"] = str(directory / "cases")
            env["E2E_COVERAGE_DIR"] = str(directory / "coverage")
            if not env.get("E2E_RANDOM_SEED"):
                env["E2E_RANDOM_SEED"] = str(int.from_bytes(os.urandom(4), "little") & 2147483647)
        else:
            parent = self.root / "artifacts/winvm"
            parent.mkdir(parents=True, exist_ok=True)
            vm_dir = Path(tempfile.mkdtemp(prefix="run-", dir=parent))
            flow_dir = Path(tempfile.mkdtemp(prefix="run-flow-", dir=parent))
            (directory / "vm-artifacts").symlink_to(vm_dir, target_is_directory=True)
            (directory / "flow-artifacts").symlink_to(flow_dir, target_is_directory=True)
            env["WINVM_RUN_DIR"] = str(vm_dir)
            env["WINVM_FLOW_RUN_DIR"] = str(flow_dir)
            env["WINVM_PREPARED_DIR"] = str(self.session / "winvm-prepared")
            env["WINVM_FAIL_FAST"] = "0"
            commands = [["./dev/winvm/run.sh", "test"],
                        ["./dev/winvm/flow-run.sh"]]
        started = time.monotonic()
        self.say(f"{kind}: loop {index} starting (completed={stats['completed']}, failed={stats['failed']})")
        statuses = []
        for number, argv in enumerate(commands, 1):
            if self.stop.is_set():
                break
            statuses.append(self.command(argv, directory / f"command-{number}.log", env))
        elapsed = time.monotonic() - started
        interrupted = self.stop.is_set()
        failed = any(status != 0 for status in statuses)
        names = failure_names(kind, directory, vm_dir)
        result = "interrupted" if interrupted else "failed" if failed else "passed"
        if interrupted:
            stats["interrupted"] += 1
        else:
            stats["completed"] += 1
            if failed:
                stats["failed"] += 1
                stats["failures"].update(names or ["unidentified test or infrastructure failure"])
            else:
                stats["successful_seconds"] += elapsed
        metadata = dict(kind=kind, loop=index, result=result, seconds=elapsed,
                        commands=commands, exit_codes=statuses, failed_tests=names,
                        environment={key: env[key] for key in (
                            "E2E_RANDOM_SEED", "E2E_LOG_DIR", "E2E_COVERAGE_DIR",
                            "WINVM_RUN_DIR", "WINVM_FLOW_RUN_DIR",
                            "WINVM_PREPARED_DIR", "WINVM_FAIL_FAST") if key in env},
                        artifacts=str(directory) if failed or interrupted else None)
        with self.lock:
            with (self.session / "loops.jsonl").open("a") as output:
                output.write(json.dumps(metadata) + "\n")
        if failed or interrupted:
            (directory / "result.json").write_text(json.dumps(metadata, indent=2) + "\n")
        else:
            shutil.rmtree(directory)
            if vm_dir:
                shutil.rmtree(vm_dir)
            if flow_dir:
                shutil.rmtree(flow_dir)
        self.say(f"{kind}: loop {index} {result} ({elapsed:.1f}s; completed={stats['completed']}, failed={stats['failed']})")

    def report(self):
        lines = ["Test cycle report", f"Stop: {self.reason}",
                 f"Started (UTC): {self.started}",
                 f"Finished (UTC): {datetime.datetime.now(datetime.timezone.utc).isoformat()}",
                 f"Host: {self.context['host']}", f"Repository: {self.root}",
                 f"Revision: {self.context.get('revision', 'see preparation.log')}",
                 f"Worktree: {self.context.get('worktree', 'see preparation.log')}",
                 "Flags: " + shlex.join(f"{k}={v}" for k, v in self.flags.items()),
                 "Go: " + " ".join(GO_FLAGS) + "; race enabled for lib and bin"]
        for kind, stats in self.stats.items():
            if kind not in self.enabled:
                lines.append(f"{kind} tests: disabled")
                continue
            successful = stats["completed"] - stats["failed"]
            average = f"{stats['successful_seconds'] / successful:.1f}s" if successful else "n/a"
            unidentified = stats["failures"].get("unidentified test or infrastructure failure", 0)
            named = sum(stats["failures"].values()) - unidentified
            lines.append(f"{kind}: loops started={stats['started']}, completed={stats['completed']}, "
                         f"failed loops={stats['failed']}, failed tests/cases={named}, "
                         f"unidentified failures={unidentified}, interrupted={stats['interrupted']}, "
                         f"average successful loop={average}")
            for name, count in stats["failures"].most_common():
                lines.append(f"  {count}x {name}")
        lines.append(f"Session logs and loop index: {self.session}")
        if any(s["failed"] or s["interrupted"] for s in self.stats.values()):
            lines.append("Failure and interrupted-loop output: <session>/<type>/loop-*/")
            if self.stats["winvm"]["failed"] or self.stats["winvm"]["interrupted"]:
                lines.append("Windows disks and logs: vm-artifacts link in each retained Windows loop.")
        else:
            lines.append("No failed or interrupted loop artifacts.")
        lines += ["Counts exclude interrupted loops; package/infrastructure failures are labelled.",
                  "Reproduce: use the flags above with just test-cycles; see loops.jsonl for exact commands."]
        lines.extend(self.errors)
        report = "\n".join(lines) + "\n"
        (self.session / "report.txt").write_text(report)
        self.say(report.rstrip())

    def run(self):
        threads = []
        self.say("Flags: " + shlex.join(f"{k}={v}" for k, v in self.flags.items()))
        self.say("winvm tests: " + ("enabled" if "winvm" in self.enabled else "disabled"))
        try:
            if self.prepare():
                for kind in self.enabled:
                    thread = threading.Thread(target=self.loop, args=(kind,))
                    threads.append(thread)
                    thread.start()
                for thread in threads:
                    thread.join()
        except Exception:
            self.errors.append(traceback.format_exc())
            self.reason = "runner error"
        finally:
            self.stop.set()
            for thread in threads:
                thread.join()
            self.report()
        return 1 if self.errors else 0


def main():
    parser = argparse.ArgumentParser(
        description=__doc__,
        epilog="Use just test-cycles to load .env. Logs: artifacts/test-cycles/. "
               "Set RUN_WINVM_TESTS=1 to enable Windows VM tests.")
    parser.parse_args()
    runner = Runner()
    for number in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
        signal.signal(number, runner.signal)
    return runner.run()


if __name__ == "__main__":
    raise SystemExit(main())
