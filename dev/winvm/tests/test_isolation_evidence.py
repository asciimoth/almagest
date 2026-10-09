import copy
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest


TOOL = Path(__file__).resolve().parents[1] / "tools/isolation-evidence.py"
SPEC = importlib.util.spec_from_file_location("isolation_evidence", TOOL)
EVIDENCE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(EVIDENCE)


class IsolationEvidenceTests(unittest.TestCase):
    def fixture(self, mutate=None):
        checks = {name: {"ok": True, "detail": "expected"} for name in EVIDENCE.REQUIRED}
        values = {}
        for profile, elevated in (("caller", False), ("service", True)):
            values[f"windows-isolation-{profile}/result.json"] = {
                "schemaVersion": 1, "profile": profile, "checks": copy.deepcopy(checks),
            }
            values[f"windows-isolation-{profile}/environment.json"] = {
                "launcherElevated": elevated, "build": 20348, "architecture": "amd64",
            }
        if mutate:
            mutate(values)
        temporary = tempfile.NamedTemporaryFile(suffix=".tar", delete=False)
        temporary.close()
        with tarfile.open(temporary.name, "w") as archive:
            for name, value in values.items():
                data = json.dumps(value).encode()
                member = tarfile.TarInfo("artifacts/" + name)
                member.size = len(data)
                archive.addfile(member, io.BytesIO(data))
        self.addCleanup(Path(temporary.name).unlink)
        return temporary.name

    def test_accepts_both_profiles(self):
        EVIDENCE.validate(self.fixture())

    def test_rejects_failed_or_missing_checks_and_wrong_authority(self):
        mutations = [
            lambda values: values["windows-isolation-caller/result.json"]["checks"]
                ["host_file_denied"].update(ok=False),
            lambda values: values["windows-isolation-service/result.json"]["checks"]
                .pop("ipc_write"),
            lambda values: values["windows-isolation-caller/environment.json"]
                .update(launcherElevated=True),
        ]
        for mutate in mutations:
            with self.subTest(mutate=mutate), self.assertRaises(ValueError):
                EVIDENCE.validate(self.fixture(mutate))


if __name__ == "__main__":
    unittest.main()
