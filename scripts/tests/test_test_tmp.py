"""Check test scratch selection and cleanup."""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().parents[1] / "with-test-tmp.sh"


class TestScratchTests(unittest.TestCase):
    def test_explicit_tmpdir_is_preserved(self):
        with tempfile.TemporaryDirectory() as directory:
            result = subprocess.run(
                ["bash", str(SCRIPT), sys.executable, "-c",
                 "import os; print(os.environ['TMPDIR'])"],
                env={**os.environ, "TMPDIR": directory},
                capture_output=True, text=True, check=True,
            )
            self.assertEqual(result.stdout.strip(), directory)
            self.assertTrue(Path(directory).is_dir())

    @unittest.skipUnless(sys.platform.startswith("linux"), "Linux tmpfs selection")
    def test_private_scratch_is_removed_after_failure(self):
        env = dict(os.environ)
        env.pop("TMPDIR", None)
        env.pop("GOTMPDIR", None)
        env["ALMAGEST_TEST_TMPFS"] = "1"
        result = subprocess.run(
            ["bash", str(SCRIPT), sys.executable, "-c",
             "import json, os, sys; print(json.dumps(dict(os.environ))); sys.exit(7)"],
            env=env, capture_output=True, text=True,
        )
        self.assertEqual(result.returncode, 7, result.stderr)
        child = json.loads(result.stdout)
        if "TMPDIR" not in child:
            self.skipTest("no writable executable tmpfs on this host")
        self.assertFalse(Path(child["TMPDIR"]).exists())
        self.assertEqual(child["GOTMPDIR"], "/tmp")


if __name__ == "__main__":
    unittest.main()
