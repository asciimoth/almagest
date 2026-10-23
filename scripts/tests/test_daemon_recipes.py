"""Check daemon development recipe mode and flag composition."""

import itertools
import os
import subprocess
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
RUNNER = ROOT / "scripts/run-daemon.sh"


class DaemonRecipeTests(unittest.TestCase):
    def run_runner(self, *arguments):
        environment = os.environ.copy()
        environment["ALMAGEST_DAEMON_PRINT_COMMAND"] = "1"
        return subprocess.run(
            [str(RUNNER), *arguments],
            cwd=ROOT,
            env=environment,
            capture_output=True,
            text=True,
            check=False,
        )

    def test_daemon_recipe_builds_only_the_required_subprogram_launcher(self):
        result = subprocess.run(
            ["just", "--dry-run", "daemon"],
            cwd=ROOT,
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        output = result.stdout + result.stderr
        self.assertIn("go build -o ./almagestd", output)
        self.assertNotIn("build/isolation-linux", output)
        self.assertIn("almagest-subprogram-launcher", output)
        self.assertIn("libseccomp", output)
        self.assertNotIn("almagest-linux-role-launcher", output)

    def test_default_is_direct_and_uses_local_development_options(self):
        result = self.run_runner("")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(str(ROOT / "almagestd"), result.stdout)
        for required in (
            "--no-isolation",
            "/local",
            "--unsafe-config",
            "--tmp-unix-admin",
            "--discover-system-programs",
        ):
            self.assertIn(required, result.stdout)
        self.assertNotIn("--experimental", result.stdout)
        self.assertIn(
            'export ALMAGEST_EXAMPLES_DIR="$repo_root/examples"', RUNNER.read_text()
        )

    def test_explicit_direct_mode_is_compatible(self):
        result = self.run_runner("--no-isolation")
        self.assertEqual(result.returncode, 0, result.stderr)
        for required in (
            "--no-isolation",
            "/local",
            "--unsafe-config",
            "--tmp-unix-admin",
            "--discover-system-programs",
        ):
            self.assertIn(required, result.stdout)
        self.assertNotIn("--experimental", result.stdout)

    def test_default_direct_mode_accepts_experimental(self):
        result = self.run_runner("--experimental")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("--no-isolation", result.stdout)
        self.assertIn("--experimental", result.stdout)

    def test_direct_experimental_flags_are_order_independent(self):
        outputs = set()
        for flags in itertools.permutations(("--no-isolation", "--experimental")):
            result = self.run_runner(" ".join(flags))
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("--no-isolation", result.stdout)
            self.assertIn("--experimental", result.stdout)
            outputs.add(result.stdout)
        self.assertEqual(len(outputs), 1)

    def test_unknown_flags_fail_before_launch(self):
        result = self.run_runner("--no-isolation --unknown")
        self.assertEqual(result.returncode, 2)
        self.assertIn("unsupported daemon recipe flag: --unknown", result.stderr)

    def test_log_level_replaces_development_default(self):
        for flag in ("-l", "--lvl"):
            result = self.run_runner("--sudo", f"{flag} info")
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("-l info", result.stdout)
            self.assertNotIn("-l debug", result.stdout)

    def test_log_level_requires_a_supported_value(self):
        missing = self.run_runner("--sudo", "--lvl")
        self.assertEqual(missing.returncode, 2)
        self.assertIn("flag --lvl requires a value", missing.stderr)

        unsupported = self.run_runner("--sudo", "--lvl trace")
        self.assertEqual(unsupported.returncode, 2)
        self.assertIn("unsupported daemon log level: trace", unsupported.stderr)

    def test_sudo_isolation_uses_root_owned_stage(self):
        result = self.run_runner("--sudo", "")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("sudo -- systemd-run --scope", result.stdout)
        self.assertIn("Delegate=yes", result.stdout)
        self.assertIn("ALMAGEST_ISOLATION_DEVELOPMENT_ROOT=", result.stdout)
        self.assertIn("ALMAGEST_ISOLATION_DEVELOPMENT_IDENTITIES=", result.stdout)
        self.assertIn(f"ALMAGEST_EXAMPLES_DIR={ROOT / 'examples'}", result.stdout)
        self.assertIn("/var/lib/almagest/dev-", result.stdout)
        self.assertIn("/run/almagest-dev-", result.stdout)
        self.assertIn("--isolation=auto", result.stdout)
        self.assertIn("--discover-system-programs", result.stdout)
        self.assertNotIn("build/isolation-linux/almagestd", result.stdout)

    def test_sudo_experimental_requires_explicit_direct_mode(self):
        result = self.run_runner("--sudo", "--experimental")
        self.assertEqual(result.returncode, 2)
        self.assertIn("requires explicit --no-isolation", result.stderr)

    def test_sudo_direct_mode_does_not_stage(self):
        result = self.run_runner("--sudo", "--no-isolation")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("sudo --", result.stdout)
        self.assertIn("build/isolation-linux/almagestd", result.stdout)
        self.assertNotIn("/run/almagest-dev-", result.stdout)
        self.assertNotIn("--tmp-unix-admin", result.stdout)


if __name__ == "__main__":
    unittest.main()
