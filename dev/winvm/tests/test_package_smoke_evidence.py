"""Tests for package-smoke evidence validation."""

import hashlib
import importlib.util
import io
import json
import sys
import tarfile
import tempfile
import unittest
from pathlib import Path
from unittest import mock


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "package_smoke_evidence", ROOT / "tools/package-smoke-evidence.py"
)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class PackageSmokeEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.archive = self.root / "artifacts.tar"
        self.output = self.root / "output"

    def write_archive(self, user_outcome="passed", extra_name=None):
        msi = b"test msi"
        digest = hashlib.sha256(msi).hexdigest()
        files = {
            "artifacts/package-build.json": json.dumps({
                "package": "almagest_1.2.3_windows_amd64.msi",
                "sha256": digest,
            }).encode(),
            "artifacts/package-smoke.json": json.dumps({
                "schemaVersion": 1,
                "suite": "windows-msi-package-smoke",
                "outcome": "privileged-passed",
                "imageVariant": "package-clean",
                "preconditions": {"clean": True},
                "service": {"state": "Running"},
                "elevatedCli": "passed",
                "administrationGroup": {
                    "initialMemberCount": 0, "pipeAccess": "passed"},
                "package": "almagest_1.2.3_windows_amd64.msi",
            }).encode(),
            "artifacts/package-user-smoke.json": json.dumps({
                "schemaVersion": 1,
                "suite": "windows-msi-package-smoke-user",
                "outcome": user_outcome,
                "programFilesWriteDenied": True,
                "machinePipeAccessGranted": True,
                "protectedConfigReadDenied": True,
            }).encode(),
            "artifacts/almagest_1.2.3_windows_amd64.msi": msi,
        }
        if extra_name:
            files[extra_name] = b"unsafe"
        with tarfile.open(self.archive, "w") as archive:
            directory = tarfile.TarInfo("artifacts")
            directory.type = tarfile.DIRTYPE
            archive.addfile(directory)
            for name, data in files.items():
                info = tarfile.TarInfo(name)
                info.size = len(data)
                archive.addfile(info, io.BytesIO(data))

    def run_validator(self):
        with mock.patch.object(sys, "argv", [
                "package-smoke-evidence.py", str(self.archive), str(self.output)]):
            MODULE.main()

    def test_accepts_and_extracts_complete_evidence(self):
        self.write_archive()
        self.run_validator()
        evidence = json.loads((self.output / "package-smoke.json").read_text())
        self.assertEqual(evidence["outcome"], "passed")
        self.assertTrue((self.output / "package-user-smoke.json").is_file())

    def test_rejects_failed_user_check_and_unsafe_member(self):
        self.write_archive(user_outcome="failed")
        with self.assertRaises(AssertionError):
            self.run_validator()
        self.write_archive(extra_name="../escape")
        with self.assertRaises(AssertionError):
            self.run_validator()


if __name__ == "__main__":
    unittest.main()
