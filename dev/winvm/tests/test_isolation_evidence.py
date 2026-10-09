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

    def test_requires_data_path_candidates_when_requested(self):
        def add_data_path(values):
            for profile in ("caller", "service"):
                values[f"windows-isolation-{profile}/result.json"]["dataPath"] = {
                    "schemaVersion": 1,
                    "runs": 7,
                    "logMode": "normal",
                    "matcherMalformedRejected": True,
                    "matcherFreshTupleAccepted": True,
                    "matcherNoObservationLease": True,
                    "controlResponseP99NS": 100,
                    "saturatedDataCompleted": True,
                    "measurements": [
                        {"candidate": "in-process-native", "droppedPackets": 0},
                        {"candidate": "broker-relay", "droppedPackets": 0},
                        {"candidate": "shared-memory-relay", "droppedPackets": 0},
                        {"candidate": "in-process-native-matcher",
                         "droppedPackets": 0},
                        {"candidate": "bounded-matcher-rpc",
                         "droppedPackets": 0},
                    ],
                }
                if profile == "service":
                    values[f"windows-isolation-{profile}/result.json"]["dataPath"]["measurements"] += [
                        {"candidate": "broker-relay", "transport": "tun",
                         "family": "ipv4", "workload": "plain",
                         "droppedPackets": 0},
                        {"candidate": "broker-relay", "transport": "tun",
                         "family": "ipv6", "workload": "wireguard-shaped",
                         "droppedPackets": 0},
                    ]
                for log_mode in ("disabled", "overloaded"):
                    extra = copy.deepcopy(values[f"windows-isolation-{profile}/result.json"])
                    extra["dataPath"]["logMode"] = log_mode
                    values[f"windows-isolation-{profile}/result-{log_mode}.json"] = extra

        EVIDENCE.validate(self.fixture(add_data_path), require_data_path=True)
        with self.assertRaises(ValueError):
            EVIDENCE.validate(self.fixture(), require_data_path=True)

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
