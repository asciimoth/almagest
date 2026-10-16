import importlib.util
from pathlib import Path
import unittest


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


if __name__ == "__main__":
    unittest.main()
