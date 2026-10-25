"""Validate the frozen Windows MSI contract without building an MSI."""

import base64
import hashlib
import json
import re
import unittest
import uuid
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
CONTRACT_PATH = ROOT / "packaging" / "windows" / "contract.json"
FLAKE_PATH = ROOT / "flake.nix"


def sri_to_hex(value):
    algorithm, encoded = value.split("-", 1)
    if algorithm != "sha256":
        raise ValueError(f"unsupported SRI algorithm: {algorithm}")
    return base64.b64decode(encoded).hex()


class WindowsPackageContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.contract = json.loads(CONTRACT_PATH.read_text())
        cls.flake = FLAKE_PATH.read_text()

    def test_identity_and_component_guids_are_stable_and_unique(self):
        identity = self.contract["identity"]
        self.assertEqual(identity["upgradeCode"],
                         str(uuid.UUID(identity["upgradeCode"])).upper())

        components = self.contract["components"]
        guid_values = [value for key, value in components.items()
                       if key != "generatedGuidPolicy"]
        self.assertEqual(len(guid_values), len(set(guid_values)))
        for value in guid_values:
            self.assertEqual(value, str(uuid.UUID(value)).upper())

    def test_contract_is_amd64_only(self):
        target = self.contract["target"]
        self.assertEqual(target, {
            "operatingSystem": "Windows Server 2022",
            "build": 20348,
            "architecture": "amd64",
            "installerArchitecture": "x64",
        })
        serialized = json.dumps(self.contract)
        self.assertNotIn('"arm64"', serialized)
        self.assertNotIn('"386"', serialized)

    def test_service_contract_matches_daemon_dispatcher(self):
        service = self.contract["service"]
        self.assertEqual(service["name"], "almagestd")
        self.assertEqual(service["account"], "LocalSystem")
        self.assertEqual(service["arguments"], ["--isolation=off"])
        self.assertEqual(service["lifecycle"], {
            "progressIntervalMilliseconds": 2000,
            "startWaitHintMilliseconds": 30000,
            "stopWaitHintMilliseconds": 30000,
            "shutdownTimeoutMilliseconds": 120000,
            "stopAndShutdownShareCancellation": True,
        })
        self.assertEqual(
            [action["delayMilliseconds"] for action in service["recovery"]["actions"]],
            [5000, 15000, 60000],
        )
        self.assertTrue(service["recovery"]["repeatLastAction"])
        self.assertFalse(service["recovery"]["reboot"])

    def test_service_log_contract(self):
        logging = self.contract["logging"]
        self.assertTrue(logging["serviceOnly"])
        self.assertEqual(logging["path"],
                         r"%ProgramData%\Almagest\logs\almagestd.log")
        self.assertEqual(logging["timestamps"], "UTC")
        self.assertEqual(logging["rotationMaxBytes"], 20 * 1024 * 1024)
        self.assertEqual(logging["retainedOldFiles"], 5)
        self.assertEqual(logging["directoryAndFileAccess"],
                         ["LocalSystem", "Administrators"])
        self.assertTrue(logging["preserveInteractiveConsole"])
        self.assertTrue(logging["preserveOnUninstall"])

    def test_mutable_state_is_retained(self):
        retention = self.contract["retention"]
        expected = ["config", "protectedConfig", "store", "logs"]
        for operation in ("repair", "upgrade", "uninstall"):
            self.assertEqual(retention[operation], expected)
        self.assertFalse(retention["overwriteExistingConfig"])
        self.assertFalse(retention["purgeFeature"])

    def test_wintun_pin_matches_flake(self):
        wintun = self.contract["drivers"]["wintun"]
        self.assertIn(f'url = "{wintun["archiveUrl"]}";', self.flake)
        self.assertIn(f'hash = "{wintun["archiveNixSha256"]}";', self.flake)
        self.assertEqual(sri_to_hex(wintun["archiveNixSha256"]),
                         wintun["archiveSha256"])
        self.assertRegex(
            self.flake,
            rf"unpacked/wintun/bin/amd64/{re.escape(wintun['file'])}\s+"
            rf"\$out/amd64/{re.escape(wintun['file'])}",
        )

    def test_mullvad_pin_matches_flake(self):
        driver = self.contract["drivers"]["mullvadSplitTunnel"]
        self.assertIn(f'driverRevision = "{driver["binaryRevision"]}";', self.flake)
        self.assertIn('${driverRevision}/${architecture}-pc-windows-msvc/split-tunnel/${file}',
                      self.flake)
        for filename, record in driver["files"].items():
            pattern = re.compile(
                rf'fetchDriverFile "x86_64" "{re.escape(filename)}" '
                rf'"{re.escape(record["nixSha256"])}"',
            )
            self.assertRegex(self.flake, pattern)
            self.assertEqual(sri_to_hex(record["nixSha256"]), record["sha256"])

    def test_checked_in_dependency_hashes_match_available_nix_outputs(self):
        """Check local Nix outputs when this test runs in the development shell."""
        import os

        roots = {
            "wintun": os.environ.get("SYSNET_WINDOWS_WINTUN_DIR"),
            "mullvad": os.environ.get("MULLVAD_SPLIT_TUNNEL_DRIVER_DIR"),
        }
        if not all(roots.values()):
            self.skipTest("Windows dependency Nix outputs are not in the environment")

        expected = {
            Path(roots["wintun"]) / "amd64" / "wintun.dll":
                self.contract["drivers"]["wintun"]["sha256"],
        }
        driver = self.contract["drivers"]["mullvadSplitTunnel"]
        expected.update({
            Path(roots["mullvad"]) / "amd64" / name: record["sha256"]
            for name, record in driver["files"].items()
        })
        for path, digest in expected.items():
            with self.subTest(path=path):
                self.assertTrue(path.is_file())
                self.assertEqual(hashlib.sha256(path.read_bytes()).hexdigest(), digest)


if __name__ == "__main__":
    unittest.main()
