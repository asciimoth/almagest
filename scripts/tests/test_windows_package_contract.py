"""Validate the frozen Windows MSI contract without building an MSI."""

import base64
import hashlib
import json
import re
import subprocess
import tempfile
import unittest
import uuid
import xml.etree.ElementTree as ET
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
CONTRACT_PATH = ROOT / "packaging" / "windows" / "contract.json"
FLAKE_PATH = ROOT / "flake.nix"
WIX_PATH = ROOT / "packaging" / "windows" / "almagest.wxs"
PAYLOAD_WIX_PATH = ROOT / "packaging" / "windows" / "payload.wxs"
FIXTURE_BUILD_PATH = ROOT / "packaging" / "windows" / "build-fixture.ps1"
WIX = "{http://wixtoolset.org/schemas/v4/wxs}"
DIFX = "{http://wixtoolset.org/schemas/v4/wxs/difxapp}"
UI = "{http://wixtoolset.org/schemas/v4/wxs/ui}"


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
        cls.wix = ET.parse(WIX_PATH).getroot()
        cls.payload_wix = ET.parse(PAYLOAD_WIX_PATH).getroot()

    def test_identity_and_component_guids_are_stable_and_unique(self):
        identity = self.contract["identity"]
        self.assertEqual(identity["upgradeCode"],
                         str(uuid.UUID(identity["upgradeCode"])).upper())
        for name in ("productCodeNamespace", "packageCodeNamespace"):
            self.assertEqual(identity[name], str(uuid.UUID(identity[name])).upper())

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

    def test_wix_product_and_upgrade_policy_match_contract(self):
        package = self.wix.find(f"{WIX}Package")
        self.assertIsNotNone(package)
        self.assertEqual(package.attrib["Name"], "Almagest")
        self.assertEqual(package.attrib["Manufacturer"],
                         self.contract["identity"]["manufacturer"])
        self.assertEqual(package.attrib["ProductCode"], "$(var.ProductCode)")
        self.assertEqual(package.attrib["UpgradeCode"],
                         self.contract["identity"]["upgradeCode"])
        self.assertEqual(package.attrib["Scope"], "perMachine")
        upgrade = package.find(f"{WIX}MajorUpgrade")
        self.assertEqual(upgrade.attrib["AllowSameVersionUpgrades"], "no")
        self.assertIn("newer version", upgrade.attrib["DowngradeErrorMessage"])

    def test_wix_service_matches_contract(self):
        component = self._component(self.wix, "DaemonServiceComponent")
        service = component.find(f"{WIX}ServiceInstall")
        contract = self.contract["service"]
        self.assertEqual(service.attrib["Name"], contract["name"])
        self.assertEqual(service.attrib["DisplayName"], contract["displayName"])
        self.assertEqual(service.attrib["Account"], contract["account"])
        self.assertEqual(service.attrib["Type"], contract["type"])
        self.assertEqual(service.attrib["Start"], "auto")
        self.assertEqual(service.attrib["ErrorControl"], contract["errorControl"])
        self.assertEqual(service.attrib["Arguments"], "--isolation=off")

        config = service.find(f"{WIX}ServiceConfig")
        self.assertEqual(config.attrib["DelayedAutoStart"], "yes")
        self.assertEqual(config.attrib["FailureActionsWhen"],
                         "failedToStopOrReturnedError")
        self.assertIsNone(service.find(f"{WIX}ServiceConfigFailureActions"))
        actions = {item.attrib["Id"]: item for item in
                   self.wix.findall(f".//{WIX}CustomAction")}
        command = (r'"[SystemFolder]sc.exe" failure almagestd reset= 86400 '
                   r'actions= restart/5000/restart/15000/restart/60000')
        for action_id, execution in (
                ("RollbackAlmagestServiceRecovery", "rollback"),
                ("ConfigureAlmagestServiceRecovery", "deferred")):
            action = actions[action_id]
            self.assertEqual(action.attrib["ExeCommand"], command)
            self.assertEqual(action.attrib["Execute"], execution)
            self.assertEqual(action.attrib["Impersonate"], "no")
            self.assertEqual(action.attrib["Return"], "check")
        sequence = {item.attrib["Action"]: item for item in
                    self.wix.findall(f".//{WIX}InstallExecuteSequence/{WIX}Custom")}
        self.assertEqual(sequence["RollbackAlmagestServiceRecovery"].attrib,
                         {"Action": "RollbackAlmagestServiceRecovery",
                          "After": "CreateAlmagestAdministrationGroup",
                          "Condition": 'NOT (REMOVE ~= "ALL")'})
        self.assertEqual(sequence["ConfigureAlmagestServiceRecovery"].attrib,
                         {"Action": "ConfigureAlmagestServiceRecovery",
                          "After": "RollbackAlmagestServiceRecovery",
                          "Condition": 'NOT (REMOVE ~= "ALL")'})
        control = component.find(f"{WIX}ServiceControl")
        self.assertEqual(control.attrib, {
            "Id": "AlmagestServiceControl",
            "Name": "almagestd",
            "Start": "install",
            "Stop": "both",
            "Remove": "uninstall",
            "Wait": "yes",
        })
        self.assertEqual(service.findall(f"{WIX}ServiceDependency"), [])

    def test_desktop_app_features_match_contract(self):
        policy = self.contract["desktopApp"]
        self.assertTrue(policy["installedByDefault"])
        self.assertTrue(policy["autoStartByDefault"])
        self.assertTrue(policy["serviceOnlyInstallSupported"])

        package = self.wix.find(f"{WIX}Package")
        features = {feature.attrib["Id"]: feature for feature in
                    package.findall(f".//{WIX}Feature")}
        self.assertEqual(features["BackendFeature"].attrib["AllowAbsent"], "no")
        self.assertEqual(features[policy["optionalFeature"]].attrib["Level"], "1")
        self.assertEqual(features[policy["autoStartOptionalFeature"]].attrib["Level"], "1")
        self.assertIsNotNone(package.find(f"{UI}WixUI[@Id='WixUI_FeatureTree']"))

        app = self._component(self.wix, "DesktopAppComponent")
        self.assertEqual(app.attrib["Guid"], self.contract["components"]["desktopApp"])
        self.assertEqual(app.find(f"{WIX}File").attrib["Source"],
                         r"$(var.PayloadDir)\almagest-app.exe")
        auto_start = self._component(self.wix, "DesktopAppAutoStartComponent")
        self.assertEqual(auto_start.attrib["Guid"],
                         self.contract["components"]["desktopAppAutoStart"])
        value = auto_start.find(f"{WIX}RegistryValue")
        self.assertEqual(value.attrib["Root"], "HKLM")
        self.assertEqual(value.attrib["Name"], "Almagest")
        self.assertEqual(value.attrib["Value"],
                         r'"[#DesktopAppExecutable]" --background')

    def test_wix_driver_policy_is_explicit(self):
        component = self._component(self.wix, "MullvadDriverComponent")
        files = {item.attrib["Source"].rsplit("\\", 1)[-1]
                 for item in component.findall(f"{WIX}File")}
        self.assertEqual(files, {
            "mullvad-split-tunnel.inf",
            "mullvad-split-tunnel.sys",
            "mullvad-split-tunnel.cat",
        })
        driver = component.find(f"{DIFX}Driver")
        self.assertEqual(driver.attrib, {
            "AddRemovePrograms": "no",
            "DeleteFiles": "yes",
            "ForceInstall": "no",
            "Legacy": "no",
            "PlugAndPlayPrompt": "no",
            "Sequence": "1",
        })
        policy = self.contract["drivers"]["mullvadSplitTunnel"]
        self.assertEqual(policy["declaredInfStart"], "demand")
        self.assertFalse(policy["daemonHardDependency"])
        self.assertTrue(policy["stagedFilesRemovedOnUninstall"])
        self.assertNotIn("phase4Decisions", policy)

        service_component = self._component(
            self.wix, "MullvadDriverServiceComponent")
        self.assertEqual(service_component.attrib["Guid"],
                         self.contract["components"]["mullvadDriverService"])
        system_driver = service_component.find(f"{WIX}File")
        self.assertEqual(system_driver.attrib["Source"],
                         r"$(var.MullvadDir)\mullvad-split-tunnel.sys")
        actions = {item.attrib["Id"]: item for item in
                   self.wix.findall(f".//{WIX}CustomAction")}
        self.assertIn("type= kernel start= demand error= normal",
                      actions["CreateMullvadDriverService"].attrib["ExeCommand"])
        self.assertIn("start= demand error= normal",
                      actions["ConfigureMullvadDriverService"].attrib["ExeCommand"])
        self.assertEqual(actions["RollbackMullvadDriverService"].attrib["Execute"],
                         "rollback")
        self.assertEqual(actions["DeleteMullvadDriverService"].attrib["Execute"],
                         "deferred")

    def test_wix_installs_only_the_wintun_distribution_dll(self):
        component = self._component(self.wix, "WintunComponent")
        files = component.findall(f"{WIX}File")
        self.assertEqual(len(files), 1)
        self.assertEqual(files[0].attrib["Source"],
                         r"$(var.WintunDir)\wintun.dll")
        source = WIX_PATH.read_text().lower()
        self.assertNotIn("wintun.sys", source)
        self.assertNotIn("wintun.inf", source)
        self.assertNotIn("wintun.cat", source)

    def test_wix_installs_documentation_and_dependency_licenses(self):
        component = self._component(self.wix, "PackageDocumentationComponent")
        files = {item.attrib["Id"]: item.attrib["Source"]
                 for item in component.findall(f"{WIX}File")}
        self.assertEqual(files, {
            "ProjectLicense": r"$(var.SourceDir)\LICENSE",
            "PackageReadme":
                r"$(var.SourceDir)\packaging\windows\README.md",
            "WintunLicense":
                r"$(var.WintunDir)\..\licenses\wintun-LICENSE.txt",
            "MullvadLicense":
                r"$(var.MullvadDir)\..\licenses\mullvad-LICENSE-MPL.txt",
        })
        self.assertIn("unpacked/wintun/LICENSE.txt", self.flake)
        mullvad = self.contract["drivers"]["mullvadSplitTunnel"]
        self.assertIn(mullvad["sourceRevision"], self.flake)
        self.assertIn("sha256-ZqMQfVrWoFiqt1PqrCBHzLLtDjlGXdD+WETaPjANUXI=",
                      self.flake)

    def test_program_data_component_is_permanent(self):
        component = self._component(self.wix, "ProgramDataAclComponent")
        self.assertEqual(component.attrib["Permanent"], "yes")
        self.assertEqual(component.attrib["Guid"],
                         self.contract["components"]["programDataAcl"])
        permission = component.find(f"{WIX}CreateFolder/{WIX}PermissionEx")
        self.assertEqual(permission.attrib,
                         {"Sddl": "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"})

    def test_wix_creates_and_preserves_administration_group(self):
        authorization = self.contract["authorization"]
        self.assertEqual(authorization, {
            "localGroup": "Almagest Administrators",
            "machinePipeAccess": [
                "LocalSystem", "Administrators", "Almagest Administrators"],
            "groupPreservedOnUninstall": True,
            "installerSelectsMembers": False,
            "inferInteractiveUser": False,
        })
        actions = {item.attrib["Id"]: item for item in
                   self.wix.findall(f".//{WIX}CustomAction")}
        action = actions["CreateAlmagestAdministrationGroup"]
        self.assertIn("net.exe", action.attrib["ExeCommand"])
        self.assertIn(authorization["localGroup"], action.attrib["ExeCommand"])
        self.assertIn("||", action.attrib["ExeCommand"])
        self.assertEqual(action.attrib["Execute"], "deferred")
        self.assertEqual(action.attrib["Impersonate"], "no")
        self.assertEqual(action.attrib["Return"], "check")
        sequence = {item.attrib["Action"]: item for item in
                    self.wix.findall(f".//{WIX}InstallExecuteSequence/{WIX}Custom")}
        self.assertEqual(sequence["CreateAlmagestAdministrationGroup"].attrib, {
            "Action": "CreateAlmagestAdministrationGroup",
            "After": "ConfigureMullvadDriverService",
            "Condition": 'NOT (REMOVE ~= "ALL")',
        })

    def test_fixture_build_is_x64_and_uses_required_extensions(self):
        source = FIXTURE_BUILD_PATH.read_text()
        self.assertIn("'-arch', 'x64'", source)
        self.assertIn("'WixToolset.Util.wixext'", source)
        self.assertIn("'WixToolset.DifxApp.wixext'", source)
        self.assertNotIn("arm64", source.lower())
        self.assertIn("almagest.wxs", source)
        self.assertIn("payload.wxs", source)

    def test_reproducible_build_path_is_locked_and_records_evidence(self):
        lock = json.loads((ROOT / "dev/winvm/image-lock.json").read_text())
        self.assertEqual(lock["dotnet"]["version"], "6.0.36")
        self.assertEqual(lock["rust"]["version"], "1.90.0")
        self.assertEqual(lock["python"]["version"], "3.13.1")
        self.assertEqual(lock["llvmMingw"]["version"], "20261020")
        for name in ("wix", "wixUtil", "wixDifxApp", "wixDtf", "wixUi"):
            self.assertEqual(lock[name]["version"], "4.0.6")
            self.assertRegex(lock[name]["sha256"], r"^[0-9a-f]{64}$")

        provision = (ROOT / "dev/winvm/provision.ps1").read_text()
        for token in ("@@DOTNET_FILE@@", "@@PYTHON_FILE@@", "@@RUST_FILE@@",
                      "@@LLVM_MINGW_FILE@@",
                      "@@WIX_FILE@@", "@@WIX_UTIL_FILE@@",
                      "@@WIX_DIFXAPP_FILE@@", "@@WIX_DTF_FILE@@",
                      "@@WIX_UI_FILE@@"):
            self.assertIn(token, provision)
        self.assertIn("difxapp_x64.wixlib", provision)
        package = (ROOT / "dev/winvm/package.ps1").read_text()
        self.assertIn("package-build.json", package)
        self.assertIn("difxapp_x64.wixlib", package)
        self.assertIn("Get-FileHash -Algorithm SHA256", package)
        self.assertIn("WixToolset.Util.wixext.dll", package)
        self.assertIn("WixToolset.DifxApp.wixext.dll", package)
        self.assertIn("WixToolset.UI.wixext.dll", package)
        self.assertIn("WixToolset.Dtf.WindowsInstaller.dll", package)
        self.assertIn("$summary.RevisionNumber", package)
        self.assertIn("$summary.CreateTime", package)
        self.assertIn("$summary.LastSaveTime", package)
        self.assertIn("[IO.File]::SetLastWriteTime", package)
        self.assertIn("CompoundFile.RootEntryTimes", package)
        self.assertIn("$directoryOffset + 108", package)
        self.assertIn("WindowsInstaller.SummaryInfo]::new", package)
        self.assertNotIn("$database.Commit()", package)
        self.assertNotIn("Dependency verification failed: $LASTEXITCODE", package)

        runner = (ROOT / "dev/winvm/run.sh").read_text()
        supervisor = (ROOT / "dev/winvm/supervise.py").read_text()
        self.assertIn("$mode == package", runner)
        self.assertIn('self.args.mode == "package"', supervisor)
        self.assertIn("tools/package-evidence.py", supervisor)
        self.assertIn("run.sh\" package", (ROOT / "dev/winvm/repro-package.sh").read_text())

    def test_package_smoke_uses_clean_image_and_privilege_boundaries(self):
        provision = (ROOT / "dev/winvm/provision.ps1").read_text()
        builder = (ROOT / "dev/winvm/build-image.sh").read_text()
        runner = (ROOT / "dev/winvm/run.sh").read_text()
        supervisor = (ROOT / "dev/winvm/supervise.py").read_text()
        smoke = (ROOT / "dev/winvm/package-smoke.ps1").read_text()
        user = (ROOT / "dev/winvm/package-user-smoke.ps1").read_text()

        self.assertIn("@@INSTALL_RUNTIME_DEPENDENCIES@@", provision)
        self.assertIn("imageVariant=$imageVariant", provision)
        self.assertIn("package-clean", builder)
        self.assertIn("base_key \"$image_variant\"", runner)
        self.assertIn('phases = ("package", "package-install", "package-user")',
                      supervisor)
        self.assertIn('phase in ("privileged", "resource", "package-install",',
                      supervisor)
        self.assertIn("tools/package-smoke-evidence.py", supervisor)
        for requirement in (
                "msiexec.exe", "Get-AuthenticodeSignature", "Get-PEMachine",
                "Win32_Service", "DelayedAutoStart", "sc.exe qfailure",
                "FailureActionsOnNonCrashFailures", "pipe://machine",
                "installed-files.json", "installed-acls.txt", "daemon.log"):
            self.assertIn(requirement, smoke)
        for requirement in ("Almagest Administrators", "Get-LocalGroup",
                            "Get-LocalGroupMember", "pipeAccess"):
            self.assertIn(requirement, smoke)
        self.assertIn("programFilesWriteDenied", user)
        self.assertIn("machinePipeAccessGranted", user)
        self.assertIn("protectedConfigReadDenied", user)

    def test_package_lifecycle_covers_retention_and_msi_transitions(self):
        runner = (ROOT / "dev/winvm/run.sh").read_text()
        stage = (ROOT / "dev/winvm/stage.sh").read_text()
        supervisor = (ROOT / "dev/winvm/supervise.py").read_text()
        prepare = (ROOT / "dev/winvm/package-lifecycle-prepare.ps1").read_text()
        finish = (ROOT / "dev/winvm/package-lifecycle-finish.ps1").read_text()

        self.assertIn("$mode == package-lifecycle", runner)
        self.assertIn("package-upgrade-manifest.json", stage)
        self.assertIn('"package-lifecycle-prepare"', supervisor)
        self.assertIn('"package-reboot"', supervisor)
        self.assertIn('"package-lifecycle-finish"', supervisor)
        self.assertIn("tools/package-lifecycle-evidence.py", supervisor)
        for token in ("kv", "secrets", "snapshots", "001-hello-network", "Stop-Service",
                      "Start-Service", "package-lifecycle-prepare.json"):
            self.assertIn(token, prepare)
        for token in ("Stop-Process", "/fvomus", "msiexec-upgrade.log",
                      "msiexec-downgrade.log", "'/x'", "driverRemoved",
                      "retainedStateUsable", "administrationGroupRetained"):
            self.assertIn(token, finish)

    def test_package_integration_uses_installed_service_and_native_tun(self):
        runner = (ROOT / "dev/winvm/run.sh").read_text()
        supervisor = (ROOT / "dev/winvm/supervise.py").read_text()
        integration = (ROOT / "dev/winvm/package-integration.ps1").read_text()

        self.assertIn("$mode == package-integration", runner)
        self.assertIn('"package-integration"', supervisor)
        self.assertIn("tools/package-integration-evidence.py", supervisor)
        self.assertNotIn("dev\\winvm\\almagestd.exe", integration)
        self.assertNotIn("dev\\winvm\\almagest-cli.exe", integration)
        for token in (
                "ProgramFiles", "pipe://machine", "admin', 'hello", "docs', 'tree",
                "config', 'apply", "LocalProxy 1101", "OutNetwork 1103", "kv', 'set",
                "secrets', 'set", "002-mock-network", "almagest/Tun",
                "Win32_PnPSignedDriver", "Stop-Service almagestd",
                "package-integration.json"):
            self.assertIn(token, integration)

    def test_project_integration_exposes_release_and_opt_in_test_gates(self):
        justfile = (ROOT / "justfile").read_text()
        selected = (ROOT / "scripts/test-selected.py").read_text()
        assembly = ROOT / "scripts/package-windows.sh"
        source = assembly.read_text()

        for recipe in ("package-windows version=''", "test-windows-packaging version=''",
                       "test-windows-packaging-enabled", "lint-windows-packaging"):
            self.assertIn(recipe, justfile)
        self.assertIn("RUN_WINDOWS_PKG_TESTS", justfile)
        self.assertIn("./scripts/package-windows.sh", justfile)
        self.assertIn('selector.startswith("winpkg:")', selected)
        for name in ("build", "smoke", "lifecycle", "integration"):
            self.assertIn(f'"{name}"', selected)
        self.assertIn('"$repo_root/dev/winvm/run.sh" package', source)
        self.assertIn('install -m 0644 "$msi"', source)
        self.assertIn('install -m 0644 "$checksum"', source)
        self.assertIn('install -m 0644 "$evidence"', source)
        self.assertIn('"$output_dir/WINDOWS_PACKAGE_CONTENTS.md"', source)
        version = subprocess.run(
            ["bash", str(assembly), "--print-version", "7.8.9"],
            cwd=ROOT,
            check=True,
            capture_output=True,
            text=True,
        )
        self.assertEqual(version.stdout, "7.8.9\n")

    def test_wix_linter_accepts_tracked_package_sources(self):
        subprocess.run(
            ["python3", str(ROOT / "scripts/lint-windows-packaging.py")],
            cwd=ROOT,
            check=True,
            capture_output=True,
            text=True,
        )

    def test_build_manifest_identities_are_deterministic_and_input_bound(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for directory in ("packaging/windows", "dev/winvm", "locales", "examples"):
                (root / directory).mkdir(parents=True, exist_ok=True)
            (root / "packaging/windows/contract.json").write_bytes(
                CONTRACT_PATH.read_bytes())
            for name in ("LICENSE", "packaging/windows/README.md",
                         "dev/winvm/almagestd.exe", "dev/winvm/almagest-cli.exe",
                         "dev/winvm/almagest-app.exe",
                         "locales/value.json", "examples/example.hujson"):
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(name)
            source = root / "source.json"
            source.write_text(json.dumps({
                "sha256": "1" * 64,
                "revision": "abc123",
                "dirty": False,
            }))
            image_lock = root / "image-lock.json"
            image_lock.write_text('{"locked":true}\n')
            output = root / "manifest.json"
            command = [
                "python3", str(ROOT / "packaging/windows/create-build-manifest.py"),
                "--version", "1.2.3", "--root", str(root),
                "--source-record", str(source), "--image-lock", str(image_lock),
                "--output", str(output),
            ]
            subprocess.run(command, check=True)
            first = json.loads(output.read_text())
            subprocess.run(command, check=True)
            self.assertEqual(first, json.loads(output.read_text()))
            (root / "locales/value.json").write_text("changed")
            subprocess.run(command, check=True)
            changed = json.loads(output.read_text())
            self.assertEqual(first["productCode"], changed["productCode"])
            self.assertNotEqual(first["packageCode"], changed["packageCode"])

    @staticmethod
    def _component(root, component_id):
        component = root.find(f".//{WIX}Component[@Id='{component_id}']")
        if component is None:
            raise AssertionError(f"component not found: {component_id}")
        return component


if __name__ == "__main__":
    unittest.main()
