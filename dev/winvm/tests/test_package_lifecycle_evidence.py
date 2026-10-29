"""Tests for package-lifecycle evidence validation."""

import importlib.util
import io
import json
import tarfile
import tempfile
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "package_lifecycle_evidence", ROOT / "tools/package-lifecycle-evidence.py"
)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class PackageLifecycleEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.archive = self.root / "artifacts.tar"
        self.output = self.root / "output"

    def write_archive(self, changed=None):
        files = {
            "artifacts/package-build.json": json.dumps({
                "package": "almagest_1.2.3_windows_amd64.msi", "version": "1.2.3",
                "productCode": "BASE",
                "sha256": "ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb",
            }),
            "artifacts/package-upgrade-build.json": json.dumps({
                "package": "almagest_1.2.4_windows_amd64.msi", "version": "1.2.4",
                "productCode": "UPGRADE",
                "sha256": "3e23e8160039594a33894f6564e1b1348bbd7a0088d42c4acb73eeaed59c009d",
            }),
            "artifacts/package-smoke.json": json.dumps({
                "suite": "windows-msi-package-smoke", "outcome": "privileged-passed",
                "imageVariant": "package-clean", "preconditions": {"clean": True},
                "service": {"state": "Running"},
            }),
            "artifacts/almagest_1.2.3_windows_amd64.msi": "a",
            "artifacts/almagest_1.2.4_windows_amd64.msi": "b",
            "artifacts/package-lifecycle-prepare.json": json.dumps({
                "schemaVersion": 1, "suite": "windows-msi-package-lifecycle-prepare",
                "outcome": "passed", "baseVersion": "1.2.3", "upgradeVersion": "1.2.4",
                "stateCreated": {"kv": True, "secret": True, "snapshot": True, "graph": True},
                "serviceRestart": {"stopped": True, "processIdBefore": 10, "processIdAfter": 11},
            }),
            "artifacts/package-lifecycle.json": json.dumps({
                "schemaVersion": 1, "suite": "windows-msi-package-lifecycle", "outcome": "passed",
                "baseVersion": "1.2.3", "upgradeVersion": "1.2.4",
                "reboot": {"passed": True},
                "failureRecovery": {"passed": True, "processIdBefore": 11, "processIdAfter": 12},
                "repair": {"passed": True, "aclReconciled": True},
                "upgrade": {"passed": True, "processIdBefore": 12, "processIdAfter": 13},
                "downgrade": {"rejected": True, "exitCode": 1638},
                "uninstall": {"passed": True, "programDataRetained": True, "driverRemoved": True},
                "featureSelection": {"passed": True, "appRetained": True,
                                     "autoStartRemoved": True},
                "reinstall": {"passed": True, "retainedStateUsable": True,
                              "serviceOnly": True},
                "retained": {"config": True, "protectedConfig": True, "kv": True,
                             "secret": True, "snapshot": True, "graph": True},
            }),
        }
        if changed:
            changed(files)
        with tarfile.open(self.archive, "w") as archive:
            directory = tarfile.TarInfo("artifacts")
            directory.type = tarfile.DIRTYPE
            archive.addfile(directory)
            for name, value in files.items():
                data = value.encode()
                item = tarfile.TarInfo(name)
                item.size = len(data)
                archive.addfile(item, io.BytesIO(data))

    def run_validator(self):
        with mock.patch("sys.argv", ["package-lifecycle-evidence.py",
                                     str(self.archive), str(self.output)]):
            MODULE.main()

    def test_accepts_complete_evidence(self):
        self.write_archive()
        self.run_validator()
        self.assertTrue((self.output / "package-lifecycle.json").is_file())
        self.assertTrue((self.output / "almagest_1.2.4_windows_amd64.msi").is_file())

    def test_rejects_failed_retention(self):
        def change(files):
            lifecycle = json.loads(files["artifacts/package-lifecycle.json"])
            lifecycle["retained"]["secret"] = False
            files["artifacts/package-lifecycle.json"] = json.dumps(lifecycle)

        self.write_archive(change)
        with self.assertRaises(AssertionError):
            self.run_validator()

    def test_rejects_unsafe_archive_member(self):
        self.write_archive()
        with tarfile.open(self.archive, "a") as archive:
            item = tarfile.TarInfo("../escape")
            item.size = 1
            archive.addfile(item, io.BytesIO(b"x"))
        with self.assertRaises(AssertionError):
            self.run_validator()


if __name__ == "__main__":
    unittest.main()
