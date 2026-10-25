"""Check the lightweight source-install command."""

import os
import subprocess
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
INSTALLER = ROOT / "packaging" / "install-source.sh"


class SourceInstallTests(unittest.TestCase):
    def run_function(self, expression):
        return subprocess.run(
            [
                "bash",
                "-c",
                f"source {INSTALLER!s}; {expression}",
            ],
            cwd=ROOT,
            capture_output=True,
            text=True,
            check=False,
        )

    def test_installer_is_executable_and_has_valid_syntax(self):
        self.assertTrue(os.access(INSTALLER, os.X_OK))
        result = subprocess.run(
            ["bash", "-n", INSTALLER],
            cwd=ROOT,
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_go_version_boundaries(self):
        for version in ("1.26.9", "1.26.10", "1.27.0", "2.0.0"):
            with self.subTest(version=version):
                result = self.run_function(
                    f"version_at_least {version} 1.26.9"
                )
                self.assertEqual(result.returncode, 0, result.stderr)
        for version in ("1.25.10", "1.26.8"):
            with self.subTest(version=version):
                result = self.run_function(
                    f"version_at_least {version} 1.26.9"
                )
                self.assertNotEqual(result.returncode, 0)

    def test_node_version_ranges_match_locked_dependencies(self):
        for version in ("20.19.0", "22.12.0", "24.0.0", "26.1.0"):
            with self.subTest(version=version):
                result = self.run_function(f"node_version_supported {version}")
                self.assertEqual(result.returncode, 0, result.stderr)
        for version in ("20.18.9", "21.9.0", "22.11.0", "23.9.0"):
            with self.subTest(version=version):
                result = self.run_function(f"node_version_supported {version}")
                self.assertNotEqual(result.returncode, 0)

    def test_non_amd64_warning_describes_runtime_limits(self):
        for architecture in ("aarch64", "arm64", "riscv64"):
            with self.subTest(architecture=architecture):
                result = self.run_function(
                    f"print_architecture_warning {architecture}"
                )
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("limited support", result.stderr)
                self.assertIn("isolation", result.stderr)
                self.assertIn("sandboxing", result.stderr)

        amd64 = self.run_function("print_architecture_warning x86_64")
        self.assertEqual(amd64.returncode, 0, amd64.stderr)
        self.assertEqual(amd64.stderr, "")

    def test_just_install_uses_lightweight_installer(self):
        result = subprocess.run(
            ["just", "--dry-run", "install"],
            cwd=ROOT,
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        output = result.stdout + result.stderr
        self.assertIn("./packaging/install-source.sh", output)
        for excluded in ("nix", "docker", "goreleaser"):
            self.assertNotIn(excluded, output.lower())


if __name__ == "__main__":
    unittest.main()
