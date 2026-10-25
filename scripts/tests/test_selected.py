"""Test focused test selection without starting Docker or a Windows VM."""

import importlib.util
import io
import json
from pathlib import Path
import subprocess
import unittest
from contextlib import redirect_stderr
from unittest.mock import patch


SCRIPT = Path(__file__).resolve().parents[1] / "test-selected.py"
SPEC = importlib.util.spec_from_file_location("test_selected_runner", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)
ROOT = SCRIPT.parents[1]


class SelectedTestHarnessTests(unittest.TestCase):
    def test_parses_exact_go_e2e_winvm_js_python_and_package_tests(self):
        manifest_cases = MODULE.load_winvm_cases()
        case_id = next(iter(manifest_cases))
        selected, _ = MODULE.parse_selectors([
            "go:lib/http:TestHTTPClientServerE2EMockNetwork",
            "e2e:static_dns_records",
            f"winvm:{case_id}",
            "js:panel/tests/api_model.test.mjs",
            "python:scripts.tests.test_cycles.CycleTests.test_go_leaf_and_package_failures",
            "winpkg:integration",
        ])
        self.assertEqual(selected.go, [MODULE.GoSelection(
            "lib", "./http", "TestHTTPClientServerE2EMockNetwork")])
        self.assertEqual(selected.e2e, ["static_dns_records"])
        self.assertEqual(selected.winvm, [case_id])
        self.assertEqual(selected.javascript, ["tests/api_model.test.mjs"])
        self.assertEqual(len(selected.python), 1)
        self.assertEqual(selected.windows_package, ["integration"])

    def test_rejects_unknown_windows_package_selector(self):
        with self.assertRaisesRegex(MODULE.SelectionError,
                                    "unknown Windows package selector"):
            MODULE.parse_selectors(["winpkg:unknown"])

    def test_rejects_all_selectors_before_running_preparation(self):
        with self.assertRaisesRegex(MODULE.SelectionError, "does not exist"):
            MODULE.parse_selectors([
                "go:lib/http:TestHTTPClientServerE2EMockNetwork",
                "e2e:not-a-real-case",
            ])

    def test_exact_go_pattern_preserves_subtest_boundaries(self):
        self.assertEqual(MODULE.exact_go_pattern("TestParent/child.one"),
                         r"^TestParent$/^child\.one$")

    def test_selected_go_and_e2e_use_existing_preparation_and_runners(self):
        selected, cases = MODULE.parse_selectors([
            "go:lib/http:TestHTTPClientServerE2EMockNetwork",
            "e2e:static_dns_records",
        ])
        calls = []

        def run(argv, **kwargs):
            calls.append((argv, kwargs))
            output = ""
            if argv[:3] == ["go", "-C", "lib"]:
                output = json.dumps({
                    "Action": "run", "Test": "TestHTTPClientServerE2EMockNetwork",
                }) + "\n"
            return subprocess.CompletedProcess(argv, 0, stdout=output)

        self.assertEqual(MODULE.Harness(run=run).execute(selected, cases), 0)
        commands = [call[0] for call in calls]
        self.assertEqual(commands[:3], [
            ["just", "repair-go-work"],
            ["just", "certcat-ensure-generated"],
            ["just", "build-web"],
        ])
        self.assertIn(["./e2e/run.sh", "static_dns_records"], commands)
        go = next(command for command in commands if command[:3] == ["go", "-C", "lib"])
        self.assertIn("-race", go)
        self.assertIn("-json", go)
        e2e_call = next(call for call in calls if call[0][0] == "./e2e/run.sh")
        self.assertEqual(e2e_call[1]["env"]["E2E_COVERAGE"], "0")

    def test_preparation_failure_stops_selected_test_execution(self):
        selected, cases = MODULE.parse_selectors([
            "go:lib/http:TestHTTPClientServerE2EMockNetwork",
            "e2e:static_dns_records",
        ])
        calls = []

        def run(argv, **kwargs):
            calls.append(argv)
            return subprocess.CompletedProcess(argv, 1)

        self.assertEqual(MODULE.Harness(run=run).execute(selected, cases), 1)
        self.assertEqual(calls, [["just", "repair-go-work"]])

    def test_go_selection_fails_when_the_exact_test_does_not_run(self):
        selected, cases = MODULE.parse_selectors([
            "go:certcat:TestDNSNameMatchesSubtree",
        ])

        def run(argv, **kwargs):
            return subprocess.CompletedProcess(argv, 0, stdout="")

        with redirect_stderr(io.StringIO()):
            self.assertEqual(MODULE.Harness(run=run).execute(selected, cases), 1)

    def test_exact_fuzz_selection_runs_only_that_fuzz_target(self):
        selected, cases = MODULE.parse_selectors([
            "go:bin/almagestd:FuzzSnapshotEntryName",
        ])
        calls = []

        def run(argv, **kwargs):
            calls.append(argv)
            output = ""
            if argv[:3] == ["go", "-C", "bin"]:
                output = json.dumps({
                    "Action": "run", "Test": "FuzzSnapshotEntryName",
                }) + "\n"
            return subprocess.CompletedProcess(argv, 0, stdout=output)

        with patch.dict(MODULE.os.environ, {"FUZZ_TIME": "3s"}):
            self.assertEqual(MODULE.Harness(run=run).execute(selected, cases), 0)
        command = next(call for call in calls if call[:3] == ["go", "-C", "bin"])
        self.assertIn("-run=^$", command)
        self.assertIn("-fuzz=^FuzzSnapshotEntryName$", command)
        self.assertIn("-fuzztime=3s", command)
        self.assertNotIn("-race", command)

    def test_winvm_cases_share_one_prepared_vm_run(self):
        cases = MODULE.load_winvm_cases()
        by_privilege = {}
        for case_id, privilege in cases.items():
            by_privilege.setdefault(privilege, case_id)
        chosen = list(by_privilege.values())
        selected, cases = MODULE.parse_selectors(
            [f"winvm:{case_id}" for case_id in chosen])
        calls = []

        def run(argv, **kwargs):
            calls.append((argv, kwargs))
            return subprocess.CompletedProcess(argv, 0)

        with patch.object(MODULE.platform, "system", return_value="Linux"):
            self.assertEqual(MODULE.Harness(run=run).execute(selected, cases), 0)
        self.assertEqual([call[0] for call in calls], [
            ["./dev/winvm/ensure-image.sh"],
            ["./dev/winvm/run.sh", "test"],
        ])
        self.assertEqual(calls[1][1]["env"]["WINVM_TEST_CASES"], ",".join(chosen))
        self.assertTrue(calls[1][1]["env"]["WINVM_COVERAGE_DIR"].endswith(
            "/coverage/selected/winvm"))

    def test_matrix_selection_caps_vm_memory(self):
        selected, cases = MODULE.parse_selectors([
            "winvm:sandbox::windows-mode-matrix-elevated",
        ])
        calls = []

        def run(argv, **kwargs):
            calls.append((argv, kwargs))
            return subprocess.CompletedProcess(argv, 0)

        with patch.object(MODULE.platform, "system", return_value="Linux"):
            self.assertEqual(MODULE.Harness(run=run).execute(selected, cases), 0)
        self.assertEqual(calls[-1][1]["env"]["WINVM_MEMORY_MIB"], "4096")

    def test_winvm_interrupt_is_forwarded_without_killing_supervisor(self):
        class Process:
            pid = 4321

            def __init__(self):
                self.waits = 0

            def wait(self):
                self.waits += 1
                if self.waits == 1:
                    raise KeyboardInterrupt
                return 130

        process = Process()
        harness = MODULE.Harness()
        with patch.object(MODULE.subprocess, "Popen", return_value=process), \
             patch.object(MODULE.os, "killpg") as killpg, \
             redirect_stderr(io.StringIO()):
            self.assertFalse(harness.winvm_command(["./dev/winvm/run.sh", "privileged"], env={}))
        killpg.assert_called_once_with(process.pid, MODULE.signal.SIGINT)
        self.assertEqual(process.waits, 2)

    def test_failure_does_not_skip_later_selected_tests(self):
        selected, cases = MODULE.parse_selectors([
            "python:scripts.tests.test_cycles.CycleTests.test_go_leaf_and_package_failures",
            "python:scripts.tests.test_cycles.CycleTests.test_timeout_names_the_active_test",
        ])
        calls = []

        def run(argv, **kwargs):
            calls.append(argv)
            return subprocess.CompletedProcess(argv, 1 if len(calls) == 1 else 0)

        self.assertEqual(MODULE.Harness(run=run).execute(selected, cases), 1)
        self.assertEqual(len(calls), 2)

    def test_windows_package_selectors_use_one_explicit_version(self):
        selected, cases = MODULE.parse_selectors([
            "winpkg:build", "winpkg:smoke", "winpkg:integration",
        ])
        calls = []

        def run(argv, **kwargs):
            calls.append((argv, kwargs))
            return subprocess.CompletedProcess(argv, 0)

        with patch.dict(MODULE.os.environ, {"ALMAGEST_PACKAGE_VERSION": "2.3.4"}):
            self.assertEqual(MODULE.Harness(run=run).execute(selected, cases), 0)
        self.assertEqual([call[0] for call in calls], [
            ["just", "package-windows", "2.3.4"],
            ["just", "winvm-package-smoke", "2.3.4"],
            ["just", "winvm-package-integration", "2.3.4"],
        ])


if __name__ == "__main__":
    unittest.main()
