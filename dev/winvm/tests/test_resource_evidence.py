import importlib.util
import copy
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest


TOOL = Path(__file__).resolve().parents[1] / "tools/resource-evidence.py"
SPEC = importlib.util.spec_from_file_location("resource_evidence", TOOL)
RESOURCE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(RESOURCE)


class ResourceEvidenceTests(unittest.TestCase):
    def fixture(self, mutate=None, diagnostic=False):
        suite = {
            "schemaVersion": 1, "suite": "windows-m8-resource", "outcome": "passed",
            "platformId": "windows-server-2022-amd64", "qualifying": not diagnostic,
            "workload": {"warmupCycles": 10, "batchCount": 4, "cyclesPerBatch": 25,
                         "cancellationCycles": 100, "soakDuration": "2m" if diagnostic else "30m",
                         "diagnosticOverride": diagnostic},
            "settlingSeconds": 10, "budgets": RESOURCE.EXPECTED_BUDGETS,
            "cases": {name: "passed" for name in RESOURCE.EXPECTED_CASES},
            "failureInjection": [{"case": name, "method": "fault-injection",
                                  "retained": "owned state", "recovery": "explicit retry"}
                                 for name in RESOURCE.EXPECTED_FAULTS],
            "recoveryJournal": {"persistentCrossProcessSplitJournal": False,
                                "manualRecovery": "preserve evidence and recover verified owned state"},
        }
        native = {"schemaVersion": 1, "adapters": [], "addresses": [], "routes": [], "dns": []}
        report = {"warmupCycles": 10, "batchCount": 4, "cyclesPerBatch": 25,
                  "samples": [{"handles": 20 + index, "goroutines": 5,
                               "privateBytes": 1000000} for index in range(5)]}
        values = {"suite.json": suite, "resources-before.json": native,
                  "resources-after.json": copy.deepcopy(native), "process-resources.json": report}
        if mutate:
            mutate(values)
        temporary = tempfile.NamedTemporaryFile(suffix=".tar", delete=False)
        temporary.close()
        with tarfile.open(temporary.name, "w") as archive:
            for name, value in values.items():
                data = json.dumps(value).encode()
                member = tarfile.TarInfo("artifacts/windows-m8-resource/" + name)
                member.size = len(data)
                archive.addfile(member, io.BytesIO(data))
        self.addCleanup(Path(temporary.name).unlink)
        return temporary.name

    def test_accepts_fixed_qualification_contract(self):
        RESOURCE.validate(self.fixture())

    def test_rejects_leak_growth_and_missing_fault_evidence(self):
        mutations = [
            lambda value: value["resources-after.json"]["adapters"].append({"name": "leak"}),
            lambda value: value["process-resources.json"]["samples"][-1].update(handles=40),
            lambda value: value["suite.json"]["failureInjection"].pop(),
        ]
        for mutate in mutations:
            with self.subTest(mutate=mutate), self.assertRaises(ValueError):
                RESOURCE.validate(self.fixture(mutate))

    def test_diagnostic_soak_is_visible_but_not_qualifying(self):
        artifact = self.fixture(diagnostic=True)
        with self.assertRaises(ValueError):
            RESOURCE.validate(artifact)
        RESOURCE.validate(artifact, allow_diagnostic=True)


if __name__ == "__main__":
    unittest.main()
