"""Exercise loop independence, failure retention, and process cleanup without VMs."""

import importlib.util
import io
import json
import os
from pathlib import Path
import signal
import subprocess
import tempfile
import threading
import time
import unittest
from contextlib import redirect_stdout
from unittest.mock import patch

SCRIPT = Path(__file__).resolve().parents[1] / "test-cycles.py"
SPEC = importlib.util.spec_from_file_location("test_cycles_runner", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class CycleTests(unittest.TestCase):
    def test_timeouts_default_off_and_require_positive_finite_seconds(self):
        with tempfile.TemporaryDirectory() as temp:
            runner = MODULE.Runner(Path(temp), {})
            self.assertEqual(runner.timeouts, {
                "TEST_CYCLES_TIMEOUT": None,
                "TEST_CYCLES_SOFT_TIMEOUT": None,
            })
        for value in ("0", "-1", "invalid", "nan", "inf"):
            with self.subTest(value=value), tempfile.TemporaryDirectory() as temp:
                with self.assertRaisesRegex(ValueError, "positive number of seconds"):
                    MODULE.Runner(Path(temp), {"TEST_CYCLES_TIMEOUT": value})

    def test_hard_timeout_requests_the_normal_stop_path(self):
        with tempfile.TemporaryDirectory() as temp:
            runner = MODULE.Runner(Path(temp), {"TEST_CYCLES_TIMEOUT": "0.02"})
            watcher = threading.Thread(target=runner.watch_timeouts,
                                       args=(time.monotonic(),))
            watcher.start()
            watcher.join(timeout=1)
            self.assertFalse(watcher.is_alive())
            self.assertTrue(runner.stop.is_set())
            self.assertEqual(runner.reason, "TEST_CYCLES_TIMEOUT reached")

    def test_soft_timeout_waits_for_a_failure(self):
        with tempfile.TemporaryDirectory() as temp:
            runner = MODULE.Runner(Path(temp), {"TEST_CYCLES_SOFT_TIMEOUT": "0.02"})
            watcher = threading.Thread(target=runner.watch_timeouts,
                                       args=(time.monotonic(),))
            watcher.start()
            self.assertTrue(runner.soft_timeout_reached.wait(timeout=1))
            self.assertFalse(runner.stop.is_set())
            runner.note_failure()
            watcher.join(timeout=1)
            self.assertFalse(watcher.is_alive())
            self.assertEqual(runner.reason,
                             "TEST_CYCLES_SOFT_TIMEOUT reached after a failure")

    def test_soft_timeout_stops_at_deadline_after_an_earlier_failure(self):
        with tempfile.TemporaryDirectory() as temp:
            runner = MODULE.Runner(Path(temp), {"TEST_CYCLES_SOFT_TIMEOUT": "0.02"})
            runner.note_failure()
            self.assertFalse(runner.stop.is_set())
            watcher = threading.Thread(target=runner.watch_timeouts,
                                       args=(time.monotonic(),))
            watcher.start()
            watcher.join(timeout=1)
            self.assertFalse(watcher.is_alive())
            self.assertEqual(runner.reason,
                             "TEST_CYCLES_SOFT_TIMEOUT reached after a failure")

    def test_winvm_toggle_defaults_off_and_enables_complete_loop(self):
        with tempfile.TemporaryDirectory() as temp:
            disabled = MODULE.Runner(Path(temp), {})
            self.assertEqual(disabled.env['RUN_WINVM_TESTS'], '0')
            self.assertNotIn('winvm', disabled.enabled)
        with tempfile.TemporaryDirectory() as temp:
            enabled = MODULE.Runner(Path(temp), {'RUN_WINVM_TESTS': '1'})
            self.assertIn('winvm', enabled.enabled)

    def test_go_leaf_and_package_failures(self):
        events = [dict(Action="fail", Package="pkg", Test=test)
                  for test in ("TestParent", "TestParent/child")]
        events += [dict(Action="fail", Package=pkg) for pkg in ("pkg", "build-error")]
        self.assertEqual(MODULE.go_failures(map(json.dumps, events)),
                         {"pkg/TestParent/child", "build-error (package failure)"})

    def test_serial_uncached_go_includes_all_modules(self):
        commands = MODULE.go_commands()
        self.assertEqual([c[2] for c in commands[:3]], ["certcat", "lib", "bin"])
        for command in commands:
            for flag in ("-count=1", "-p=1", "-parallel=1", "-json"):
                self.assertIn(flag, command)

    def test_timeout_names_the_active_test(self):
        events = [dict(Action="output", Package="pkg", Test="TestStuck",
                       Output="panic: test timed out after 10m0s\n"),
                  dict(Action="fail", Package="pkg")]
        self.assertEqual(MODULE.go_failures(map(json.dumps, events)), {"pkg/TestStuck"})

    def test_vm_watchdog_failure_is_named_even_after_recovery(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / "run.json").write_text(json.dumps(dict(
                status="failed", failureKind="guest-unresponsive", recoveryOutcome="collected")))
            self.assertEqual(MODULE.failure_names("winvm", root, root),
                             ["winvm/guest-unresponsive (infrastructure failure)"])

    def test_failure_then_success_and_interruption(self):
        with tempfile.TemporaryDirectory() as temp, redirect_stdout(io.StringIO()):
            runner = MODULE.Runner(Path(temp), {})
            calls = []

            def command(argv, log, env=None):
                calls.append(argv)
                log.write_text(json.dumps(dict(Action="fail", Package="pkg", Test="TestFlake")) + "\n")
                return 1 if len(calls) == 1 else 0

            runner.command = command
            with patch.object(MODULE.time, "monotonic", side_effect=[0, 20]):
                runner.iteration("go")
            self.assertEqual(len(calls), 5, "failed module must not skip later modules")
            self.assertTrue((runner.session / "go/loop-000001/result.json").exists())
            with patch.object(MODULE.time, "monotonic", side_effect=[20, 25]):
                runner.iteration("go")
            self.assertFalse((runner.session / "go/loop-000002").exists())
            runner.stop.set()
            runner.iteration("go")
            stats = runner.stats["go"]
            self.assertEqual((stats["completed"], stats["failed"], stats["interrupted"]), (2, 1, 1))
            self.assertEqual(stats["successful_seconds"], 5)
            runner.report()
            report = (runner.session / "report.txt").read_text()
            self.assertIn("1x pkg/TestFlake", report)
            self.assertIn("winvm tests: disabled", report)
            self.assertIn("average successful loop=5.0s", report)

    def test_loops_are_independent_and_prepare_runs_once(self):
        with tempfile.TemporaryDirectory() as temp, redirect_stdout(io.StringIO()):
            runner = MODULE.Runner(Path(temp), {"RUN_WINVM_TESTS": "1"})
            entered = threading.Barrier(3)
            counts = dict(go=0, e2e=0, winvm=0)

            def iteration(kind):
                counts[kind] += 1
                if counts[kind] == 1:
                    entered.wait(timeout=3)
                if kind == "go" and counts[kind] == 3:
                    runner.stop.set()
                if kind != "go":
                    runner.stop.wait(3)

            runner.iteration = iteration
            with patch.object(runner, "prepare", return_value=True) as prepare:
                self.assertEqual(runner.run(), 0)
                prepare.assert_called_once()
            self.assertEqual(counts, dict(go=3, e2e=1, winvm=1))

    def test_signal_stops_command_children_and_allows_cleanup(self):
        with tempfile.TemporaryDirectory() as temp, redirect_stdout(io.StringIO()):
            root = Path(temp)
            runner = MODULE.Runner(root, os.environ)
            ready = root / "ready"
            cleaned = root / "cleaned"
            script = ("trap 'touch cleaned; exit 143' TERM; "
                      "sleep 120 & child=$!; echo $child > ready; wait $child")
            results = []
            worker = threading.Thread(target=lambda: results.append(runner.command(
                ["bash", "-c", script], root / "command.log")))
            worker.start()
            deadline = time.monotonic() + 5
            while not ready.exists() and time.monotonic() < deadline:
                time.sleep(0.01)
            runner.signal(signal.SIGTERM, None)
            worker.join(timeout=5)
            self.assertFalse(worker.is_alive())
            self.assertTrue(cleaned.exists())
            self.assertNotEqual(results, [0])
            # A child may remain briefly as a zombie, but cannot still run.
            state = subprocess.run(["ps", "-o", "stat=", "-p", ready.read_text().strip()],
                                   capture_output=True, text=True).stdout.strip()
            self.assertTrue(not state or state.startswith("Z"), state)

    def test_preparation_failure_never_starts_loops(self):
        with tempfile.TemporaryDirectory() as temp, redirect_stdout(io.StringIO()):
            runner = MODULE.Runner(Path(temp), {})
            with patch.object(runner, "command", return_value=127), patch.object(runner, "loop") as loop:
                self.assertEqual(runner.run(), 1)
                loop.assert_not_called()
            self.assertIn("preparation failed", (runner.session / "report.txt").read_text())

    def test_docker_scheduler_owns_case_cleanup(self):
        with tempfile.TemporaryDirectory() as temp, redirect_stdout(io.StringIO()):
            root = Path(temp)
            (root / "e2e").mkdir()
            script = root / "e2e/run.sh"
            script.write_text("""#!/usr/bin/env bash
bash -c 'trap "trap - TERM; sleep .2; touch case-cleaned; exit" TERM
touch ready
while :; do sleep .05; done' &
child=$!
trap 'sleep .05; kill -TERM "$child"; wait "$child"; exit 143' TERM
wait "$child"
""")
            script.chmod(0o755)
            runner = MODULE.Runner(root, os.environ)
            worker = threading.Thread(target=lambda: runner.command(
                ["./e2e/run.sh", "--skip-build"], root / "command.log"))
            worker.start()
            deadline = time.monotonic() + 5
            while not (root / "ready").exists() and time.monotonic() < deadline:
                time.sleep(0.01)
            runner.signal(signal.SIGTERM, None)
            worker.join(timeout=5)
            self.assertFalse(worker.is_alive())
            self.assertTrue((root / "case-cleaned").exists(),
                            "case cleanup was killed by a second signal")

    def test_vm_success_is_removed_and_failure_is_retained(self):
        with tempfile.TemporaryDirectory() as temp, redirect_stdout(io.StringIO()):
            runner = MODULE.Runner(Path(temp), {"RUN_WINVM_TESTS": "1"})
            with patch.object(runner, "command", return_value=0):
                runner.iteration("winvm")
            self.assertEqual(list((Path(temp) / "artifacts/winvm").iterdir()), [])
            with patch.object(runner, "command", return_value=1):
                runner.iteration("winvm")
            self.assertTrue((runner.session / "winvm/loop-000002/vm-artifacts").is_dir())
            self.assertTrue((runner.session / "winvm/loop-000002/flow-artifacts").is_dir())

    def test_vm_iteration_runs_native_and_packet_flow_gates(self):
        with tempfile.TemporaryDirectory() as temp, redirect_stdout(io.StringIO()):
            runner = MODULE.Runner(Path(temp), {"RUN_WINVM_TESTS": "1"})
            calls = []

            def command(argv, _log, env=None):
                calls.append((argv, env))
                return 1

            runner.command = command
            runner.iteration("winvm")
            self.assertEqual([call[0] for call in calls], [
                ["./dev/winvm/run.sh", "test"],
                ["./dev/winvm/flow-run.sh"],
            ])
            self.assertIn("WINVM_RUN_DIR", calls[0][1])
            self.assertIn("WINVM_FLOW_RUN_DIR", calls[0][1])

    def test_e2e_cycles_disable_fail_fast_even_when_environment_enables_it(self):
        with tempfile.TemporaryDirectory() as temp, redirect_stdout(io.StringIO()):
            runner = MODULE.Runner(Path(temp), {"E2E_FAIL_FAST": "1"})
            environments = []

            def command(_argv, _log, env=None):
                environments.append(env)
                return 1

            runner.command = command
            runner.iteration("e2e")
            self.assertEqual(environments[0]["E2E_FAIL_FAST"], "0")

    def test_winvm_cycles_disable_fail_fast_even_when_environment_enables_it(self):
        with tempfile.TemporaryDirectory() as temp, redirect_stdout(io.StringIO()):
            runner = MODULE.Runner(Path(temp), {
                "RUN_WINVM_TESTS": "1", "WINVM_FAIL_FAST": "1",
            })
            environments = []

            def command(_argv, _log, env=None):
                environments.append(env)
                return 1

            runner.command = command
            runner.iteration("winvm")
            self.assertEqual(environments[0]["WINVM_FAIL_FAST"], "0")

    def test_vm_supervisor_owns_interrupt_and_log_collection(self):
        with tempfile.TemporaryDirectory() as temp, redirect_stdout(io.StringIO()):
            root = Path(temp)
            scripts = root / "dev/winvm"
            scripts.mkdir(parents=True)
            script = scripts / "run.sh"
            script.write_text("""#!/usr/bin/env bash
# A collector in the supervisor group must not receive the parent's signal.
bash -c 'trap "touch collector-killed; exit 1" TERM
touch ready
sleep 1
touch guest-artifacts.tar' &
collector=$!
trap 'wait "$collector"; touch supervisor-cleaned; exit 143' TERM
wait "$collector"
""")
            script.chmod(0o755)
            runner = MODULE.Runner(root, os.environ)
            worker = threading.Thread(target=lambda: runner.command(
                ["./dev/winvm/run.sh", "test"], root / "command.log"))
            worker.start()
            deadline = time.monotonic() + 5
            while not (root / "ready").exists() and time.monotonic() < deadline:
                time.sleep(0.01)
            runner.signal(signal.SIGINT, None)
            worker.join(timeout=5)
            self.assertFalse(worker.is_alive())
            self.assertTrue((root / "guest-artifacts.tar").exists())
            self.assertTrue((root / "supervisor-cleaned").exists())
            self.assertFalse((root / "collector-killed").exists())

    def test_vm_fallback_kills_exact_process_but_not_reused_pid(self):
        with tempfile.TemporaryDirectory() as temp, redirect_stdout(io.StringIO()):
            root = Path(temp)
            run = root / "artifacts/winvm/run-stuck"
            run.mkdir(parents=True)
            (run / "run.json").write_text('{"status":"running"}')
            runner = MODULE.Runner(root, os.environ)
            vm = subprocess.Popen(["sleep", "120"], start_new_session=True)
            try:
                start = Path(f"/proc/{vm.pid}/stat").read_text().rsplit(")", 1)[1].split()[19]
                state = run / "supervisor-state.json"
                state.write_text(json.dumps(dict(qemuPid=vm.pid, qemuStartTime=str(int(start) + 1))))
                runner.stop_vm_after_supervisor_timeout(str(run))
                self.assertIsNone(vm.poll(), "fallback must not signal a reused PID")
                state.write_text(json.dumps(dict(qemuPid=vm.pid, qemuStartTime=start)))
                runner.stop_vm_after_supervisor_timeout(str(run))
                self.assertEqual(vm.wait(timeout=3), -signal.SIGKILL)
                self.assertTrue((run / "supervisor-timeout.log").exists())
                record = json.loads((run / "run.json").read_text())
                self.assertEqual(record["failureKind"], "supervisor-cleanup-timeout")
                self.assertFalse(record["finalArtifactsCollected"])
            finally:
                if vm.poll() is None:
                    vm.kill()
                    vm.wait(timeout=3)


if __name__ == "__main__":
    unittest.main()
