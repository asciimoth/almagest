#!/usr/bin/env python3
"""Host-owned VM lifetime, health checks, and bounded evidence recovery.

A recovery boot only exports evidence. It never retries tests or clears failure.
"""

import argparse
import datetime
import json
import os
from pathlib import Path
import shlex
import shutil
import signal
import subprocess
import sys
import tarfile
import time

ROOT = Path(__file__).resolve().parent


class Failure(Exception):
    def __init__(self, kind, code=1):
        super().__init__(kind)
        self.kind, self.code = kind, code


def atomic_json(path, value):
    temporary = path.with_suffix(path.suffix + ".new")
    temporary.write_text(json.dumps(value, indent=2) + "\n")
    temporary.replace(path)


def kill_group(process, sig):
    try:
        os.killpg(process.pid, sig)
    except ProcessLookupError:
        pass


def console(message, **kwargs):
    # A terminal/tee process can disappear on Ctrl+C. Evidence collection and
    # mandatory VM termination must not depend on an open console pipe.
    try:
        print(message, flush=True, **kwargs)
    except OSError:
        pass


class Supervisor:
    def __init__(self, args, config):
        self.args, self.config = args, config
        self.directory = Path(args.run_dir)
        self.sockets = Path(args.sockets).resolve()
        if not self.sockets.name.startswith("almagest-winvm."):
            raise ValueError("unsafe VM socket directory")
        self.remote = "C:/winvm/runs/" + self.directory.name
        self.qemu = None
        self.stop_signal = None
        self.cleaning = False
        self.stage = "boot"
        self.deadline = float("inf")
        self.transferred = False
        self.recovery = False
        self.progress_seen = set()
        self.collection = {"finalCollected": False, "recoveryAttempted": False,
                           "recoveryOutcome": "not-needed", "overlayRetained": True, "attempts": []}
        self.ssh = ["ssh", "-i", args.key, "-p", str(args.port),
                    "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes",
                    "-o", "StrictHostKeyChecking=yes",
                    "-o", f"UserKnownHostsFile={self.directory / 'known_hosts'}",
                    "-o", "ConnectTimeout=5", "-o", "ServerAliveInterval=5",
                    "-o", "ServerAliveCountMax=2", "-o", "LogLevel=ERROR",
                    "-o", "WarnWeakCrypto=no", "winvm@127.0.0.1"]

    def note(self, message):
        stamp = datetime.datetime.now(datetime.timezone.utc).isoformat()
        try:
            with (self.directory / "supervisor.log").open("a") as log:
                log.write(f"{stamp} stage={self.stage} {message}\n")
        except OSError as error:
            console(f"Cannot write supervisor log: {error}", file=sys.stderr)
        console(f"==> Windows VM: {message}")

    def signal(self, number, _frame):
        # Further Ctrl+C must not interrupt evidence collection or VM reaping.
        if self.stop_signal is None:
            self.stop_signal = number

    def check(self):
        if self.stop_signal and not self.cleaning:
            raise Failure("ctrl-c" if self.stop_signal == signal.SIGINT else "sigterm",
                          128 + self.stop_signal)
        if time.monotonic() >= self.deadline:
            raise Failure("cleanup-timeout" if self.cleaning else "host-test-timeout", 124)

    def command(self, argv, seconds, log="transport.log", output=None, watch=False):
        """Bound every host child and its descendants, including during cleanup."""
        self.check()
        end = min(self.deadline, time.monotonic() + seconds)
        with (self.directory / log).open("ab") as errors:
            started = time.monotonic()
            errors.write((f"\n[host {datetime.datetime.now(datetime.timezone.utc).isoformat()}] $ " +
                          shlex.join(map(str, argv)) + "\n").encode())
            errors.flush()
            process = subprocess.Popen(argv, stdin=subprocess.DEVNULL,
                                       stdout=output if output is not None else errors,
                                       stderr=errors, start_new_session=True)
            next_health = time.monotonic()
            last_healthy = next_health
            next_archive = next_health + self.config["artifactIntervalSeconds"]
            next_notice = next_health
            tail = 0
            try:
                while process.poll() is None:
                    self.check()
                    now = time.monotonic()
                    if now >= end:
                        return 124
                    if watch:
                        tail = self.print_tail(log, tail)
                        if self.qemu.poll() is not None:
                            raise Failure("qemu-exited")
                        if now >= next_health:
                            # Test progress is not guest health. A Go timeout in
                            # a responsive guest is a test failure, not a freeze.
                            healthy = self.guest_healthy()
                            if healthy:
                                last_healthy = time.monotonic()
                            elif time.monotonic() - last_healthy >= self.config["healthTimeoutSeconds"]:
                                raise Failure("guest-unresponsive", 124)
                            next_health = time.monotonic() + self.config["healthIntervalSeconds"]
                        if now >= next_notice:
                            self.note(f"host watchdog active; guest last responsive {int(time.monotonic() - last_healthy)}s ago")
                            next_notice = time.monotonic() + 30
                        if now >= next_archive:
                            self.harvest(final=False, seconds=20)
                            next_archive = time.monotonic() + self.config["artifactIntervalSeconds"]
                    time.sleep(0.1)
                return process.returncode
            finally:
                # Also remove descendants if the command parent exited first.
                kill_group(process, signal.SIGKILL)
                try:
                    process.wait(timeout=2)
                except subprocess.TimeoutExpired:
                    self.note(f"host command {process.pid} survived SIGKILL")
                errors.write((f"\n[host] exit={process.poll()} elapsed={time.monotonic() - started:.1f}s\n").encode())
                errors.flush()
                if watch:
                    self.print_tail(log, tail)

    def print_tail(self, log, offset):
        with (self.directory / log).open("rb") as stream:
            stream.seek(offset)
            data = stream.read(65536)
            if data:
                console(data.decode(errors="replace"), end="")
            return stream.tell()

    def qga(self, *arguments, seconds=5, qmp=False):
        return [str(ROOT / "tools/qga.py"), "--socket",
                str(self.sockets / ("qmp.sock" if qmp else "qga.sock")),
                "--timeout", str(seconds), *arguments]

    def guest_healthy(self):
        if self.command(self.ssh + ["cmd.exe /c exit 0"], 8, log="health.log") == 0:
            return True
        return self.command(self.qga("exec", "cmd.exe", "/c", "exit", "0", seconds=6),
                            8, log="health.log") == 0

    def start_vm(self):
        self.check()
        for name in ("qga.sock", "qmp.sock"):
            (self.sockets / name).unlink(missing_ok=True)
        command = list(self.args.qemu)
        if self.recovery:
            command = [part.replace(f"path={self.directory}/serial.log",
                                    f"path={self.directory}/serial-recovery.log") for part in command]
        with (self.directory / ("qemu-recovery.log" if self.recovery else "qemu.log")).open("ab") as log:
            self.qemu = subprocess.Popen(command, stdin=subprocess.DEVNULL,
                                         stdout=log, stderr=log, start_new_session=True)
        stat = Path(f"/proc/{self.qemu.pid}/stat").read_text().rsplit(")", 1)[1].split()
        atomic_json(self.directory / "supervisor-state.json",
                    {"supervisorPid": os.getpid(), "qemuPid": self.qemu.pid,
                     "qemuStartTime": stat[19], "recovery": self.recovery,
                     "orphanMarker": str(Path(self.args.cache_dir) / "locks/qemu-orphan")})
        self.note(f"QEMU started pid={self.qemu.pid}; recovery={self.recovery}")

    def ready(self, seconds):
        end = min(self.deadline, time.monotonic() + seconds)
        previous = None
        script = ('if (-not (Test-Path C:\\winvm\\ready)) { exit 1 }; '
                  '(Get-Content -Raw C:\\winvm\\ready) + ":" + '
                  '(Get-CimInstance Win32_OperatingSystem).LastBootUpTime.ToUniversalTime().Ticks')
        while time.monotonic() < end:
            self.check()
            if self.qemu.poll() is not None:
                raise Failure("qemu-exited-during-boot")
            with (self.directory / "ready-token.txt").open("wb") as output:
                code = self.command(self.qga("exec", "powershell.exe", "-NoProfile", "-Command", script),
                                    min(7, end - time.monotonic()), output=output, log="agent.log")
            token = (self.directory / "ready-token.txt").read_text(errors="replace").strip()
            if code == 0 and token and token == previous:
                if self.command(self.ssh + ["whoami"], min(8, end - time.monotonic()),
                                log="ssh-identity.log") == 0:
                    return
            previous = token if code == 0 else None
            time.sleep(1)
        raise Failure("guest-readiness-timeout", 124)

    def harvest(self, final, seconds=30):
        # Stream directly to the host. A frozen guest cannot leave a successful
        # status with an empty/partial archive or overwrite the last good copy.
        partial = self.directory / "guest-artifacts.partial.tar"
        failure = None
        try:
            with partial.open("wb") as output:
                code = self.command(self.ssh + [f"tar.exe -cf - -C {self.remote} artifacts"],
                                    seconds, output=output, log="collection.log")
        except Failure as error:
            failure, code = error, error.code
        valid = False
        readable = False
        try:
            with tarfile.open(partial) as archive:
                members = archive.getmembers()
                readable = bool(members)
                valid = readable and code == 0
                if readable:
                    for member in members:
                        if "/progress/" in member.name and member.isfile() and member.name not in self.progress_seen:
                            self.progress_seen.add(member.name)
                            console(archive.extractfile(member).read().decode(errors="replace").strip())
        except (OSError, tarfile.TarError):
            pass
        if valid:
            partial.replace(self.directory / "guest-artifacts.tar")
        elif readable:
            # Windows can deny reads of an active redirected stderr file.
            # Keep all other files without calling this a complete collection.
            partial.replace(self.directory / "guest-artifacts.snapshot.tar")
        self.collection["attempts"].append({"time": time.time(), "stage": self.stage,
                                             "recovery": self.recovery, "exitStatus": code,
                                             "valid": valid, "partialSnapshot": readable and not valid,
                                             "final": final})
        if final:
            self.collection["finalCollected"] = valid
        atomic_json(self.directory / "collection.json", self.collection)
        if not valid:
            self.note("partial guest snapshot retained; some files unavailable" if readable else
                      "guest archive not collected; keeping last good archive and VM disk")
        if failure:
            raise failure
        return valid

    def diagnostics(self, reason):
        log = "recovery-diagnostics.log" if self.recovery else "diagnostics.log"
        with (self.directory / log).open("a") as output:
            output.write(f"reason={reason}\nstage={self.stage}\n")
        for name in ("query-status", "query-cpus-fast", "query-blockstats"):
            self.command(self.qga("qmp", name, qmp=True, seconds=2), 3, log=log)
        self.command(self.qga("qmp", "human-monitor-command", "--arguments",
                             json.dumps({"command-line": "info registers -a"}), qmp=True, seconds=2), 3, log=log)
        self.command(self.qga("qmp", "screendump", "--arguments",
                             json.dumps({"filename": str(self.directory / ("screen-recovery.ppm" if self.recovery else "screen.ppm"))}),
                             qmp=True, seconds=2), 3, log=log)
        if self.transferred:
            self.command(self.qga("exec", "powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass",
                                 "-File", f"{self.remote}/source/dev/winvm/collect-diagnostics.ps1",
                                 "-OutputDir", f"{self.remote}/artifacts/diagnostics" + ("-recovery" if self.recovery else ""),
                                 "-Reason", reason, "-Stage", self.stage, seconds=18), 20, log=log)

    def quiesce(self):
        # Stop only this run's test launchers and their descendants. Do not kill
        # arbitrary guest PowerShell processes or a diagnostic collector.
        script = (f'$self=$PID; Get-CimInstance Win32_Process | Where-Object {{ '
                  f'$_.ProcessId -ne $self -and $_.CommandLine -like "*{self.remote}/source/dev/winvm/test-suite.ps1*" '
                  '} | ForEach-Object { taskkill.exe /PID $_.ProcessId /T /F }; exit 0')
        self.command(self.qga("exec", "powershell.exe", "-NoProfile", "-Command", script, seconds=4),
                     6, log="collection.log")

    def stop_vm(self, graceful=False):
        if self.qemu is None or self.qemu.poll() is not None:
            return True
        self.note("stopping QEMU")
        clean = False
        try:
            self.command(self.qga("shutdown", seconds=3), 4, log="agent.log")
            self.qemu.wait(timeout=min(self.config["shutdownTimeoutSeconds"] if graceful else 3,
                                       max(0, self.deadline - time.monotonic() - 6)))
            clean = True
        except (Failure, OSError, subprocess.TimeoutExpired):
            if graceful:
                try:
                    self.diagnostics("shutdown-timeout")
                    if self.transferred:
                        self.harvest(final=True)
                except (Failure, OSError):
                    pass
        # This path must work even after the diagnostic deadline has expired.
        for sig in (signal.SIGTERM, signal.SIGKILL):
            if self.qemu.poll() is not None:
                break
            self.note(f"QEMU did not stop; sending {sig.name} to pid={self.qemu.pid}")
            kill_group(self.qemu, sig)
            try:
                self.qemu.wait(timeout=3)
            except subprocess.TimeoutExpired:
                pass
        if self.qemu.poll() is None:
            stat = Path(f"/proc/{self.qemu.pid}/stat").read_text().rsplit(")", 1)[1].split()
            marker = Path(self.args.cache_dir) / "locks/qemu-orphan"
            marker.write_text(f"{self.qemu.pid} {stat[19]}\n")
            self.note("QEMU survived SIGKILL; blocking further runs")
            raise Failure("qemu-survived-kill")
        self.note(f"QEMU reaped; exit={self.qemu.returncode}; graceful={clean}")
        return clean

    def execute(self):
        self.start_vm()
        self.note("waiting for guest readiness")
        self.ready(self.config["bootTimeoutSeconds"])
        if self.args.mode == "--shell":
            process = subprocess.Popen(self.ssh[:-1] + ["-t", self.ssh[-1], "powershell.exe"])
            try:
                while process.poll() is None:
                    self.check()
                    time.sleep(0.1)
                return process.returncode
            finally:
                if process.poll() is None:
                    process.kill()
                    process.wait(timeout=3)
        self.stage = "transfer"
        self.note("transferring test payload")
        code = self.command(self.ssh + [f'powershell.exe -NoProfile -Command "New-Item -ItemType Directory -Force \'{self.remote}/source\',\'{self.remote}/artifacts\' | Out-Null"'], 30)
        if code:
            raise Failure("transfer", code)
        # scp shares all SSH identity checks but uses -P for the port.
        scp = ["scp", "-q", *self.ssh[1:-1]]
        scp[scp.index("-p")] = "-P"
        code = self.command(scp + [str(self.directory / "payload.tar"), f"{self.ssh[-1]}:{self.remote}/payload.tar"], 120)
        if code:
            raise Failure("transfer", code)
        digest = (self.directory / "payload.sha256").read_text().split()[0]
        code = self.command(self.ssh + [f'powershell.exe -NoProfile -Command "if ((Get-FileHash \'{self.remote}/payload.tar\').Hash -ne \'{digest}\') {{ exit 1 }}; tar.exe -xf {self.remote}/payload.tar -C {self.remote}/source; exit $LASTEXITCODE"'], 120)
        if code:
            raise Failure("transfer", code)
        self.transferred = True
        test_timeout = (self.config["resourceTimeoutSeconds"]
                        if self.args.mode == "resource" else self.config["testTimeoutSeconds"])
        self.deadline = time.monotonic() + test_timeout
        tests_status = 0
        if self.args.mode == "smoke":
            phases = ("smoke",)
        elif self.args.mode == "resource":
            phases = ("resource",)
        elif self.args.mode == "ordinary":
            phases = ("ordinary",)
        elif self.args.mode == "privileged":
            phases = ("privileged",)
        else:
            phases = ("ordinary", "privileged")
        for phase in phases:
            self.stage = phase if phase == "smoke" else phase + "-tests"
            self.note(f"running {phase} tests")
            if phase == "smoke":
                script = "smoke.ps1"
            elif phase == "resource":
                script = "resource-suite.ps1"
            else:
                script = "test-suite.ps1"
            args = ["powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File",
                    f"{self.remote}/source/dev/winvm/{script}", "-ArtifactDir", f"{self.remote}/artifacts"]
            if phase in ("ordinary", "privileged"):
                args += ["-Mode", phase, "-SourceDir", f"{self.remote}/source"]
            elif phase == "resource":
                args += ["-SourceDir", f"{self.remote}/source"]
                if self.args.soak_duration:
                    args += ["-SoakDuration", self.args.soak_duration]
            command = self.qga("exec", *args, seconds=test_timeout) if phase in ("privileged", "resource") else self.ssh + [" ".join(args)]
            code = self.command(command, test_timeout,
                                log=phase + "-tests.log", watch=True)
            if code == 124:
                raise Failure("host-test-timeout", 124)
            if phase != "privileged" and code == 255:
                # SSH can notice a frozen guest before the health window ends.
                # A lost transport must not start the next test phase either.
                raise Failure("guest-transport" if self.guest_healthy() else "guest-unresponsive", 255)
            if code:
                tests_status = code
        if tests_status:
            self.stage = "tests"
            raise Failure("tests", tests_status)
        self.stage = "artifacts"
        if not self.harvest(final=True):
            raise Failure("artifacts")
        if self.args.mode == "resource":
            validation = ["python3", str(ROOT / "tools/resource-evidence.py"),
                          str(self.directory / "guest-artifacts.tar")]
            if self.args.soak_duration:
                validation.append("--allow-diagnostic")
        else:
            validator = "validate_smoke_artifact" if self.args.mode == "smoke" else "validate_native_test_artifact"
            validation = ["bash", "-c", 'source "$1/common.sh"; shift; "$@"', "validate",
                          str(ROOT), validator, str(self.directory / "guest-artifacts.tar")]
            if self.args.mode in ("ordinary", "privileged"):
                validation.append(self.args.mode)
        if self.command(validation, 15, log="validation.log"):
            raise Failure("artifact-validation")
        if self.args.mode in ("test", "ordinary", "privileged"):
            if self.command(["python3", str(ROOT / "combine-coverage.py"),
                             str(self.directory / "guest-artifacts.tar"), str(ROOT.parents[1] / "coverage/winvm")], 30,
                            log="validation.log"):
                raise Failure("coverage")
        return 0

    def finish(self, failure):
        self.cleaning = True
        # One cleanup budget includes diagnosis, shutdown, and a single recovery
        # boot. Mandatory TERM/KILL/reap has a separate final six-second reserve.
        self.deadline = time.monotonic() + self.config["cleanupTimeoutSeconds"]
        try:
            if failure:
                self.note(f"failed: {failure.kind}; collecting evidence (bounded cleanup)")
                if self.transferred:
                    # Preserve test output before a diagnostic command can hang.
                    self.harvest(final=False, seconds=15)
                    self.quiesce()
                self.diagnostics(failure.kind)
                collected = self.harvest(final=True) if self.transferred else False
            else:
                collected = self.collection["finalCollected"]
            clean_stop = self.stop_vm(graceful=failure is None)
            if not clean_stop and failure is None:
                failure = Failure("shutdown")
                self.stage = "shutdown"
            if self.transferred and (not collected or (failure and failure.kind == "guest-unresponsive")):
                self.recovery = True
                self.collection["recoveryAttempted"] = True
                self.collection["recoveryOutcome"] = "incomplete"
                self.collection["finalCollected"] = False
                self.note("restarting retained VM for log recovery only; tests will NOT be retried")
                archive = self.directory / "guest-artifacts.tar"
                if archive.exists():
                    shutil.copyfile(archive, self.directory / "guest-artifacts.pre-recovery.tar")
                self.start_vm()
                self.ready(self.config["recoveryBootTimeoutSeconds"])
                # Copy existing logs first, before any new diagnostic command.
                self.harvest(final=True)
                self.diagnostics("recovery-after-" + (failure.kind if failure else "collection-failure"))
                if self.harvest(final=True):
                    self.collection["recoveryOutcome"] = "collected"
        except (Failure, OSError) as error:
            self.note(f"cleanup/recovery incomplete: {error}; retained disk contains remaining evidence")
            if failure is None:
                failure = Failure("cleanup")
        finally:
            try:
                self.stop_vm()
            except (Failure, OSError) as error:
                self.note(str(error))
                failure = failure or Failure("shutdown")
            if self.qemu is None or self.qemu.poll() is not None:
                shutil.rmtree(self.sockets)
            atomic_json(self.directory / "collection.json", self.collection)
        return failure

    def run(self):
        failure = None
        try:
            code = self.execute()
            self.check()
            if code:
                failure = Failure("shell", code)
        except Failure as error:
            failure = error
        except Exception as error:
            self.note(f"supervisor error: {error!r}")
            failure = Failure("supervisor-error")
        finally:
            if failure is None:
                self.stage = "shutdown"
            failure = self.finish(failure)
        if self.stop_signal and failure is None:
            failure = Failure("ctrl-c" if self.stop_signal == signal.SIGINT else "sigterm",
                              128 + self.stop_signal)
        if self.args.mode != "--shell":
            path = self.directory / "run.json"
            record = json.loads(path.read_text())
            record.update(status="failed" if failure else "passed", stage=self.stage,
                          exitStatus=failure.code if failure else 0,
                          failureKind=failure.kind if failure else None,
                          recoveryAttempted=self.collection["recoveryAttempted"],
                          recoveryOutcome=self.collection["recoveryOutcome"],
                          finalArtifactsCollected=self.collection["finalCollected"])
            atomic_json(path, record)
        if failure:
            console(f"VM failed at {self.stage}: {failure.kind}. Diagnostics: {self.directory}\n"
                  f"Recovery: just winvm-shell {shlex.quote(str(self.directory))}", file=sys.stderr)
        else:
            self.note(f"artifacts: {self.directory}")
        return failure.code if failure else 0


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True)
    parser.add_argument("--run-dir", required=True)
    parser.add_argument("--sockets", required=True)
    parser.add_argument("--port", required=True)
    parser.add_argument("--key", required=True)
    parser.add_argument("--cache-dir", required=True)
    parser.add_argument("--soak-duration")
    parser.add_argument("qemu", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    if args.qemu[:1] == ["--"]:
        args.qemu.pop(0)
    supervisor = Supervisor(args, json.loads((ROOT / "config.json").read_text())["machine"])
    signal.signal(signal.SIGINT, supervisor.signal)
    signal.signal(signal.SIGTERM, supervisor.signal)
    return supervisor.run()


if __name__ == "__main__":
    raise SystemExit(main())
