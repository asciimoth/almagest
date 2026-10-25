"""Tests for package-integration evidence validation."""

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
    "package_integration_evidence",
    ROOT / "tools/package-integration-evidence.py",
)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class PackageIntegrationEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.archive = self.root / "artifacts.tar"
        self.output = self.root / "output"

    def write_archive(self, *, integration_outcome="passed", extra_name=None):
        msi = b"test msi"
        digest = hashlib.sha256(msi).hexdigest()
        package = "almagest_1.2.3_windows_amd64.msi"
        files = {
            "artifacts/package-build.json": json.dumps({
                "package": package, "sha256": digest,
            }).encode(),
            "artifacts/package-smoke.json": json.dumps({
                "schemaVersion": 1, "suite": "windows-msi-package-smoke",
                "outcome": "privileged-passed", "imageVariant": "package-clean",
                "preconditions": {"clean": True}, "service": {"state": "Running"},
                "elevatedCli": "passed", "package": package,
            }).encode(),
            "artifacts/package-user-smoke.json": json.dumps({
                "schemaVersion": 1, "suite": "windows-msi-package-smoke-user",
                "outcome": "passed", "programFilesWriteDenied": True,
                "machinePipeAccessDenied": True, "protectedConfigReadDenied": True,
            }).encode(),
            "artifacts/package-integration.json": json.dumps({
                "schemaVersion": 1, "suite": "windows-msi-package-integration",
                "outcome": integration_outcome,
                "installedCli": r"C:\Program Files\Almagest\almagest-cli.exe",
                "installedService": "almagestd",
                "apiSurfaces": ["admin", "config", "dns", "docs", "engine", "kv",
                                "log", "secrets", "snapshots", "tls"],
                "localizedDocumentation": True, "packagedExamples": True,
                "configAppliedToProgramData": True, "localProxyOutNetwork": True,
                "scmRestart": {"kvRetained": True, "secretRetained": True,
                               "graphRetained": True},
                "example": {"id": "002-mock-network", "ran": True,
                            "retainedAfterRestart": True},
                "nativeTun": {"safePrivateRoute": True, "adapter": {
                    "name": "almpkg1", "signed": True,
                    "driverProvider": "WireGuard LLC", "address": "198.18.239.1/32",
                    "route": "198.19.239.0/24",
                }},
                "normalServiceStop": {"serviceStopped": True, "adapterRemoved": True,
                                      "networkConfigurationRemoved": True},
            }).encode(),
            f"artifacts/{package}": msi,
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
                "package-integration-evidence.py", str(self.archive), str(self.output)]):
            MODULE.main()

    def test_accepts_and_extracts_complete_evidence(self):
        self.write_archive()
        self.run_validator()
        self.assertTrue((self.output / "package-integration.json").is_file())
        self.assertTrue((self.output / "package-smoke.json").is_file())

    def test_rejects_failed_integration_and_unsafe_member(self):
        self.write_archive(integration_outcome="failed")
        with self.assertRaises(AssertionError):
            self.run_validator()
        self.write_archive(extra_name="../escape")
        with self.assertRaises(AssertionError):
            self.run_validator()


if __name__ == "__main__":
    unittest.main()
