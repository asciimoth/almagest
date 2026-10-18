from contextlib import redirect_stderr, redirect_stdout
import importlib.util
import io
import os
from pathlib import Path
import subprocess
import unittest
from unittest import mock


SCRIPT = Path(__file__).resolve().parents[1] / "test-fuzz.py"
SPEC = importlib.util.spec_from_file_location("test_fuzz_script", SCRIPT)
FUZZ = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(FUZZ)


class DurationTests(unittest.TestCase):
    def test_parses_go_duration(self):
        self.assertEqual(FUZZ.parse_duration("1m30.5s"), 90_500_000_000)
        self.assertEqual(FUZZ.parse_duration("250ms"), 250_000_000)

    def test_rejects_invalid_duration(self):
        for value in ("", "60", "0s", "1 second", "-1s"):
            with self.subTest(value=value):
                with self.assertRaises(ValueError):
                    FUZZ.parse_duration(value)


class DeadlineFailureTests(unittest.TestCase):
    deadline_output = """\
fuzz: elapsed: 2s, execs: 30842 (15434/sec), new interesting: 1 (total: 271)
--- FAIL: FuzzLinuxSystem (2.02s)
    context deadline exceeded
FAIL
exit status 1
FAIL  github.com/asciimoth/almagest/bin/internal/isolation/launchconfig 2.029s
"""

    def test_accepts_infrastructure_only_deadline_failure(self):
        self.assertTrue(FUZZ.is_fuzz_deadline_failure(self.deadline_output))

    def test_main_warns_for_infrastructure_only_deadline_failure(self):
        result = subprocess.CompletedProcess([], 1, self.deadline_output)
        target = ("bin", "example/package", "FuzzExample")
        with (
            mock.patch.object(FUZZ, "GO_MODULES", ("bin",)),
            mock.patch.object(FUZZ, "discover_targets", return_value=[target]),
            mock.patch.object(FUZZ, "run_streaming", return_value=result),
            mock.patch.dict(os.environ, {"FUZZ_TIME": "1s"}),
            redirect_stdout(io.StringIO()),
            redirect_stderr(io.StringIO()) as stderr,
        ):
            self.assertEqual(FUZZ.main(), 0)
        self.assertIn("WARNING: example/package.FuzzExample", stderr.getvalue())

    def test_rejects_failing_fuzz_input(self):
        output = """\
--- FAIL: FuzzLinuxSystem (0.01s)
    --- FAIL: FuzzLinuxSystem/abc (0.00s)
        system_linux_test.go:53: context deadline exceeded
Failing input written to testdata/fuzz/FuzzLinuxSystem/abc
FAIL
exit status 1
FAIL  github.com/asciimoth/almagest/bin/internal/isolation/launchconfig 0.012s
"""
        self.assertFalse(FUZZ.is_fuzz_deadline_failure(output))

    def test_rejects_other_top_level_failure(self):
        output = """\
--- FAIL: FuzzLinuxSystem (2.02s)
    unexpected callback
FAIL
exit status 1
FAIL  github.com/asciimoth/almagest/bin/internal/isolation/launchconfig 2.029s
"""
        self.assertFalse(FUZZ.is_fuzz_deadline_failure(output))


if __name__ == "__main__":
    unittest.main()
