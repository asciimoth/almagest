"""Missing or failed hostile-client observations must not pass the audit."""

import importlib.util
from pathlib import Path
import unittest


SPEC = importlib.util.spec_from_file_location(
    "security_evidence",
    Path(__file__).resolve().parents[2] / "dev/data-path/security-evidence.py",
)
EVIDENCE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(EVIDENCE)


class DataPathSecurityTests(unittest.TestCase):
    def result(self):
        return {"schema_version": 1, "profile": "linux-runtime-r-v1",
                "checks": dict.fromkeys(EVIDENCE.REQUIRED, True)}

    def test_complete_rejection_evidence(self):
        EVIDENCE.validate(self.result())

    def test_missing_observation(self):
        for name in EVIDENCE.REQUIRED:
            with self.subTest(name=name):
                result = self.result()
                del result["checks"][name]
                with self.assertRaises(ValueError):
                    EVIDENCE.validate(result)

    def test_failed_or_nonboolean_observation(self):
        for value in (False, None, 1, "true"):
            with self.subTest(value=value):
                result = self.result()
                result["checks"]["restricted_runtime"] = value
                with self.assertRaises(ValueError):
                    EVIDENCE.validate(result)

    def test_wrong_identity_or_extra_observation(self):
        for field, value in (("schema_version", 2), ("profile", "unrestricted"),
                             ("checks", {**self.result()["checks"], "extra": True})):
            with self.subTest(field=field):
                result = self.result()
                result[field] = value
                with self.assertRaises(ValueError):
                    EVIDENCE.validate(result)
