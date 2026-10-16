import tempfile
import unittest
from pathlib import Path

from configure_sandbox_mode import configure


class ConfigureSandboxModeTest(unittest.TestCase):
    def test_inserts_mode_in_hujson_object(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "protected.hujson"
            path.write_text('{\n // keep comment\n "Admin": {},\n}\n', encoding="utf-8")
            configure(path, "strict")
            value = path.read_text(encoding="utf-8")
            self.assertIn('"Sandbox": {"Mode": "strict"}', value)
            self.assertIn("// keep comment", value)

    def test_replaces_matrix_placeholder(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "protected.hujson"
            path.write_text('{"Sandbox":{"Mode":"%E2E_SANDBOX_MODE%"}}', encoding="utf-8")
            configure(path, "off")
            self.assertEqual(path.read_text(encoding="utf-8"), '{"Sandbox":{"Mode":"off"}}')

    def test_preserves_explicit_policy(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "protected.hujson"
            original = '{"Sandbox":{"Mode":"strict","Programs":{}}}'
            path.write_text(original, encoding="utf-8")
            configure(path, "auto")
            self.assertEqual(path.read_text(encoding="utf-8"), original)

    def test_preserves_case_insensitive_explicit_policy(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "protected.hujson"
            original = '{"sandbox":{"Mode":"off"}}'
            path.write_text(original, encoding="utf-8")
            configure(path, "strict")
            self.assertEqual(path.read_text(encoding="utf-8"), original)


if __name__ == "__main__":
    unittest.main()
