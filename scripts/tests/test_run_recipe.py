import os
from pathlib import Path
import subprocess
import unittest


ROOT = Path(__file__).resolve().parents[2]
RUNNER = ROOT / "scripts" / "run-recipe.sh"


class RunRecipeTests(unittest.TestCase):
    def test_runs_command_as_checkout_owner(self):
        result = subprocess.run(
            [RUNNER, "bash", "-c", "printf recipe-ok"],
            cwd=ROOT,
            check=False,
            capture_output=True,
            text=True,
        )

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "recipe-ok")

    def test_just_uses_owner_checking_runner(self):
        justfile = (ROOT / "justfile").read_text()
        self.assertIn(
            'set shell := ["./scripts/run-recipe.sh", "bash", "-euo", '
            '"pipefail", "-c"]',
            justfile,
        )

    def test_runner_is_executable(self):
        self.assertTrue(os.access(RUNNER, os.X_OK))


if __name__ == "__main__":
    unittest.main()
