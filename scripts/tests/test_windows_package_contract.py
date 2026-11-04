"""Validate the frozen Windows MSI contract without building an MSI."""

import base64
import hashlib
import json
import os
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
        self.assertEqual(service["start"], "automatic")
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
        self.assertEqual(logging["level"], "debug")
        self.assertEqual(logging["timestamps"], "UTC")
        self.assertEqual(logging["rotationMaxBytes"], 20 * 1024 * 1024)
        self.assertEqual(logging["retainedOldFiles"], 5)
        self.assertEqual(logging["directoryAndFileAccess"],
                         ["LocalSystem", "Administrators"])
        self.assertTrue(logging["preserveInteractiveConsole"])
        self.assertTrue(logging["preserveOnUninstall"])

        installer = self.contract["installerLogging"]
        self.assertEqual(installer, {
            "automatic": True,
            "mode": "voicewarmupx!",
            "path": r"%TEMP%\MSI*.LOG",
            "collectedBySupportScript": True,
        })
        package = self.wix.find(f"{WIX}Package")
        msi_logging = package.find(f"{WIX}Property[@Id='MsiLogging']")
        self.assertEqual(msi_logging.attrib["Value"], installer["mode"])

        app = self.contract["desktopAppLogging"]
        self.assertEqual(app, {
            "path": r"%LocalAppData%\Almagest\logs\almagest-app.log",
            "rotationMaxBytes": 5 * 1024 * 1024,
            "retainedOldFiles": 3,
            "collectedBySupportScript": True,
        })

    def test_mutable_state_is_retained(self):
        retention = self.contract["retention"]
        expected = ["config", "protectedConfig", "store", "logs"]
        for operation in ("repair", "upgrade", "uninstall"):
            self.assertEqual(retention[operation], expected)
        self.assertEqual(retention["reconcileAclOn"],
                         ["install", "repair", "upgrade"])
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
        self.assertEqual(config.attrib["DelayedAutoStart"], "no")
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
                          "After": "AddCurrentUserAdminAccess",
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
        files = {item.attrib["Id"]: item for item in app.findall(f"{WIX}File")}
        self.assertEqual(files["WebView2Loader"].attrib["Source"],
                         r"$(var.PayloadDir)\WebView2Loader.dll")
        shortcuts = files["DesktopAppExecutable"].findall(f"{WIX}Shortcut")
        self.assertEqual(
            {(item.attrib["Directory"], item.attrib["Name"]) for item in shortcuts},
            {("ProgramMenuFolder", "Almagest"),
             ("DesktopFolder", "Almagest")},
        )
        self.assertTrue(all(item.attrib["Advertise"] == "no"
                            for item in shortcuts))
        auto_start = self._component(self.wix, "DesktopAppAutoStartComponent")
        self.assertEqual(auto_start.attrib["Guid"],
                         self.contract["components"]["desktopAppAutoStart"])
        value = auto_start.find(f"{WIX}RegistryValue")
        self.assertEqual(value.attrib["Root"], "HKLM")
        self.assertEqual(value.attrib["Name"], "Almagest")
        self.assertEqual(value.attrib["Value"],
                         r'"[#DesktopAppExecutable]" --background')

    def test_wix_adds_install_directory_to_machine_path(self):
        component = self._component(self.wix, "PathEnvironmentComponent")
        self.assertEqual(component.attrib["Guid"],
                         self.contract["components"]["pathEnvironment"])
        environment = component.find(f"{WIX}Environment")
        self.assertEqual(environment.attrib, {
            "Id": "AlmagestPathEnvironment",
            "Name": "PATH",
            "Value": "[INSTALLFOLDER]",
            "Action": "set",
            "Part": "last",
            "System": "yes",
        })

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

    def test_wix_installs_dependency_licenses(self):
        component = self._component(self.wix, "PackageDocumentationComponent")
        files = {item.attrib["Id"]: item.attrib["Source"]
                 for item in component.findall(f"{WIX}File")}
        self.assertEqual(files, {
            "ProjectLicense": r"$(var.SourceDir)\LICENSE",
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

        package = self.wix.find(f"{WIX}Package")
        license_variable = package.find(f"{WIX}WixVariable[@Id='WixUILicenseRtf']")
        self.assertEqual(license_variable.attrib["Value"],
                         r"$(var.PayloadDir)\LICENSE.rtf")
        generator = ROOT / "packaging/windows/create-license-rtf.py"
        with tempfile.TemporaryDirectory() as temporary:
            source = Path(temporary) / "LICENSE"
            output = Path(temporary) / "LICENSE.rtf"
            source.write_text("License {text} \\ value\nsecond line\n", encoding="utf-8")
            subprocess.run(["python3", str(generator), str(source), str(output)],
                           check=True)
            rendered = output.read_text(encoding="ascii")
            self.assertTrue(rendered.startswith(r"{\rtf1\ansi"))
            self.assertIn(r"License \{text\} \\ value\par", rendered)

    def test_program_data_component_is_permanent(self):
        component = self._component(self.wix, "ProgramDataAclComponent")
        self.assertEqual(component.attrib["Permanent"], "yes")
        self.assertEqual(component.attrib["Guid"],
                         self.contract["components"]["programDataAcl"])
        permission = component.find(f"{WIX}CreateFolder/{WIX}PermissionEx")
        self.assertEqual(permission.attrib,
                         {"Sddl": "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"})

        actions = {item.attrib["Id"]: item for item in
                   self.wix.findall(f".//{WIX}CustomAction")}
        owner = actions["ReconcileProgramDataOwner"]
        self.assertIn("/setowner *S-1-5-18 /T /C /L /Q",
                      owner.attrib["ExeCommand"])
        reset = actions["ResetProgramDataAcl"]
        self.assertIn("/reset /T /C /L /Q", reset.attrib["ExeCommand"])
        protect = actions["ProtectProgramDataAcl"]
        self.assertIn("/inheritance:r /grant:r", protect.attrib["ExeCommand"])
        self.assertIn("*S-1-5-18:(OI)(CI)(F)", protect.attrib["ExeCommand"])
        self.assertIn("*S-1-5-32-544:(OI)(CI)(F)", protect.attrib["ExeCommand"])
        rollback = actions["RollbackProtectProgramDataAcl"]
        for action in (rollback, owner, reset, protect):
            command = action.attrib["ExeCommand"]
            self.assertEqual(action.attrib["Directory"], "System64Folder")
            self.assertIn('"[System64Folder]icacls.exe"', command)
            self.assertIn('"[PROGRAMDATAFOLDER]."', command)
            self.assertNotIn('"[PROGRAMDATAFOLDER]"', command)
        for action in (owner, reset, protect):
            self.assertEqual(action.attrib["Execute"], "deferred")
            self.assertEqual(action.attrib["Impersonate"], "no")
            self.assertEqual(action.attrib["Return"], "check")
        self.assertEqual(rollback.attrib["Execute"], "rollback")
        self.assertEqual(rollback.attrib["Impersonate"], "no")
        self.assertEqual(rollback.attrib["Return"], "ignore")
        sequence = {item.attrib["Action"]: item for item in
                    self.wix.findall(f".//{WIX}InstallExecuteSequence/{WIX}Custom")}
        self.assertEqual(sequence["RollbackProtectProgramDataAcl"].attrib, {
            "Action": "RollbackProtectProgramDataAcl",
            "After": "ConfigureMullvadDriverService",
            "Condition": 'NOT (REMOVE ~= "ALL")',
        })
        self.assertEqual(sequence["ReconcileProgramDataOwner"].attrib["After"],
                         "RollbackProtectProgramDataAcl")
        self.assertEqual(sequence["ResetProgramDataAcl"].attrib["After"],
                         "ReconcileProgramDataOwner")
        self.assertEqual(sequence["ProtectProgramDataAcl"].attrib["After"],
                         "ResetProgramDataAcl")

    def test_wix_creates_and_preserves_administration_group(self):
        authorization = self.contract["authorization"]
        self.assertEqual(authorization, {
            "localGroup": "Almagest Administrators",
            "machinePipeAccess": [
                "LocalSystem", "Administrators", "Almagest Administrators"],
            "groupPreservedOnUninstall": True,
            "currentUserAccessFeature": "CurrentUserAdminAccessFeature",
            "currentUserAccessByDefault": True,
            "membershipActivation": (
                "Sign out and sign in again before an unelevated process uses "
                "the administration pipe."),
            "groupMembershipPreservedOnUninstall": True,
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
            "After": "ProtectProgramDataAcl",
            "Condition": 'NOT (REMOVE ~= "ALL")',
        })
        package = self.wix.find(f"{WIX}Package")
        feature = package.find(
            f".//{WIX}Feature[@Id='{authorization['currentUserAccessFeature']}']")
        self.assertEqual(feature.attrib["Level"], "1")
        self.assertIn("Sign out and sign in", feature.attrib["Description"])
        component = self._component(self.wix, "CurrentUserAdminAccessComponent")
        self.assertEqual(component.attrib["Guid"],
                         self.contract["components"]["currentUserAdminAccess"])
        self.assertEqual(sequence["AddCurrentUserAdminAccess"].attrib, {
            "Action": "AddCurrentUserAdminAccess",
            "After": "CreateAlmagestAdministrationGroup",
            "Condition": ('&CurrentUserAdminAccessFeature = 3 AND '
                          'NOT (REMOVE ~= "ALL")'),
        })
        membership = actions["AddCurrentUserAdminAccess"]
        self.assertEqual(membership.attrib["Directory"], "System64Folder")
        self.assertTrue(membership.attrib["ExeCommand"].startswith(
            r'"[System64Folder]WindowsPowerShell\v1.0\powershell.exe"'))
        self.assertIn("[ALMAGEST_ACCESS_SID]", membership.attrib["ExeCommand"])
        self.assertIn("Get-LocalGroupMember", membership.attrib["ExeCommand"])
        self.assertIn("S-1-5-18", membership.attrib["ExeCommand"])
        self.assertIn("New-Object -TypeName System.Security.Principal.SecurityIdentifier",
                      membership.attrib["ExeCommand"])
        self.assertNotIn("[System.Security", membership.attrib["ExeCommand"])
        self.assertEqual(membership.attrib["Execute"], "deferred")
        self.assertEqual(membership.attrib["Impersonate"], "no")

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
        toolchain = ("dotnet", "rust", "python", "llvmMingw", "wix", "wixUtil",
                     "wixDifxApp", "wixDtf", "wixUi")
        for name in toolchain:
            self.assertTrue(lock[name]["version"])
            self.assertRegex(lock[name]["sha256"], r"^[0-9a-f]{64}$")
        self.assertEqual(len({lock[name]["version"] for name in toolchain[4:]}), 1)

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
        self.assertIn("cargo-vendor\\webview2-com-sys-*\\x64\\WebView2Loader.dll",
                      package)
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
        for token in ("kv", "secrets", "snapshots", "add-node", "Stop-Service",
                      "Start-Service", "package-lifecycle-prepare.json"):
            self.assertIn(token, prepare)
        for token in ("Stop-Process", "/fvomus", "msiexec-upgrade.log",
                      "msiexec-downgrade.log", "'/x'", "driverRemoved",
                      "retainedStateUsable", "administrationGroupRetained",
                      "Assert-PrivateProgramDataAcl", "aclReconciled"):
            self.assertIn(token, finish)

    def test_windows_support_collector_stays_outside_package_output(self):
        package_script = (ROOT / "scripts/package-windows.sh").read_text()
        collector = (ROOT / "scripts/collect-windows-support.ps1").read_text()
        self.assertNotIn('install -m 0644 "$repo_root/scripts/collect-windows-support.ps1"',
                         package_script)
        for token in ("MSI*.LOG", "almagest-app.log*", "almagestd.log*",
                      "Get-ProfilePathForSid", "OriginalSid"):
            self.assertIn(token, collector)
        self.assertIn("process command lines", collector)
        self.assertNotIn("CommandLine", collector)

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
                "secrets', 'set", "snapshots', 'load", "almagest/Tun",
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
        self.assertIn("./scripts/package-signing.sh prepare dist", justfile)
        self.assertIn("./scripts/package-signing.sh sign dist", justfile)
        self.assertIn('selector.startswith("winpkg:")', selected)
        for name in ("build", "smoke", "lifecycle", "integration"):
            self.assertIn(f'"{name}"', selected)
        self.assertIn('"$repo_root/dev/winvm/run.sh" package', source)
        self.assertIn('install -m 0644 "$msi"', source)
        self.assertIn('install -m 0644 "$checksum"', source)
        self.assertNotIn('install -m 0644 "$evidence"', source)
        self.assertNotIn('install -m 0644 "$repo_root/packaging/windows/README.md"', source)
        version = subprocess.run(
            ["bash", str(assembly), "--print-version", "7.8.9"],
            cwd=ROOT,
            check=True,
            capture_output=True,
            text=True,
        )
        self.assertEqual(version.stdout, "7.8.9\n")

    def test_release_signing_includes_all_windows_artifacts(self):
        signer = ROOT / "scripts/sign-windows-artifacts.sh"
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary)
            base = output / "almagest_1.2.3_windows_amd64"
            artifacts = [
                Path(f"{base}.msi"),
                output / "almagest_1.2.3_windows_amd64.msi.sha256",
            ]
            for artifact in artifacts:
                artifact.write_text(artifact.name)
            fake_gpg = output / "gpg"
            fake_gpg.write_text("#!/bin/sh\ntouch \"$5\"\n")
            fake_gpg.chmod(0o755)

            subprocess.run(
                ["bash", str(signer), str(output)],
                cwd=ROOT,
                check=True,
                env={**os.environ, "GPG_COMMAND": str(fake_gpg)},
            )

            for artifact in artifacts:
                self.assertTrue(Path(f"{artifact}.sig").is_file())

    def test_package_signing_waits_for_explicit_command_and_checks_inputs(self):
        signer = ROOT / "scripts/package-signing.sh"
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / "dist"
            output.mkdir()
            linux = output / "almagest_1.2.3_linux_amd64.deb"
            linux.write_bytes(b"linux package")
            (output / "checksums.txt").write_text(
                f"{hashlib.sha256(linux.read_bytes()).hexdigest()}  {linux.name}\n"
            )
            windows = [
                output / "almagest_1.2.3_windows_amd64.msi",
                output / "almagest_1.2.3_windows_amd64.msi.sha256",
            ]
            for artifact in windows:
                artifact.write_text(artifact.name)
            windows[1].write_text(
                f"{hashlib.sha256(windows[0].read_bytes()).hexdigest()}  {windows[0].name}\n"
            )
            fake_gpg = output / "gpg"
            fake_gpg.write_text(
                '#!/bin/sh\nwhile [ "$1" != --output ]; do shift; done\n'
                'shift\n: > "$1"\n'
            )
            fake_gpg.chmod(0o755)
            env = {**os.environ, "GPG_COMMAND": str(fake_gpg),
                   "ALMAGEST_SIGNING_MANIFEST": str(Path(temporary) / "signing-inputs.sha256")}
            subprocess.run(["bash", str(signer), "prepare", str(output)],
                           cwd=ROOT, env=env, check=True, capture_output=True)
            self.assertFalse(list(output.glob("*.sig")))
            self.assertFalse((output / ".signing-inputs.sha256").exists())

            windows[0].write_text("changed")
            rejected = subprocess.run(["bash", str(signer), "sign", str(output)],
                                      cwd=ROOT, env=env, capture_output=True)
            self.assertNotEqual(rejected.returncode, 0)
            self.assertFalse(list(output.glob("*.sig")))

            windows[0].write_text(windows[0].name)
            subprocess.run(["bash", str(signer), "sign", str(output)],
                           cwd=ROOT, env=env, check=True, capture_output=True)
            for artifact in [linux, output / "checksums.txt", *windows]:
                self.assertTrue(Path(f"{artifact}.sig").is_file())

    def test_package_output_prunes_build_files(self):
        script = ROOT / "scripts/prune-package-output.sh"
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary)
            package = output / "almagest_1.2.3_linux_amd64.deb"
            package.write_bytes(b"package")
            (output / "checksums.txt").write_text(
                f"{hashlib.sha256(package.read_bytes()).hexdigest()}  {package.name}\n"
            )
            for name in ("artifacts.json", "metadata.json", "config.yaml",
                         "PACKAGE_CONTENTS.md", "WINDOWS_PACKAGE_CONTENTS.md",
                         "collect-windows-support.ps1", "almagest_1.2.3_windows_amd64.build.json",
                         "almagest_1.2.3_linux_amd64.deb.sig", ".signing-inputs.sha256"):
                (output / name).write_text("build file")
            raw_binary = output / "almagestd-linux-amd64"
            raw_binary.mkdir()
            (raw_binary / "almagestd").write_text("raw binary")

            subprocess.run(["bash", str(script), str(output)],
                           cwd=ROOT, check=True, capture_output=True)
            self.assertEqual({p.name for p in output.iterdir()},
                             {package.name, "checksums.txt"})

    def test_wix_linter_accepts_tracked_package_sources(self):
        subprocess.run(
            ["python3", str(ROOT / "scripts/lint-windows-packaging.py")],
            cwd=ROOT,
            check=True,
            capture_output=True,
            text=True,
        )

    def test_payload_excludes_development_only_locales(self):
        payload = PAYLOAD_WIX_PATH.read_text()
        self.assertIn(r"locales\en\manifest.json", payload)
        self.assertNotIn("locales\\lorem\\", payload)
        self.assertNotIn(r"locales\README.md", payload)

    def test_build_manifest_identities_are_deterministic_and_input_bound(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for directory in ("packaging/windows", "dev/winvm", "locales", "examples"):
                (root / directory).mkdir(parents=True, exist_ok=True)
            (root / "packaging/windows/contract.json").write_bytes(
                CONTRACT_PATH.read_bytes())
            for name in ("LICENSE", "locales/README.md",
                         "dev/winvm/almagestd.exe", "dev/winvm/almagest-cli.exe",
                         "dev/winvm/almagest-app.exe", "dev/winvm/WebView2Loader.dll",
                         "dev/winvm/LICENSE.rtf",
                         "examples/example.hujson"):
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(name)
            for locale, development_only in (("en", False), ("lorem", True)):
                directory = root / "locales" / locale
                directory.mkdir()
                (directory / "manifest.json").write_text(json.dumps({
                    "format": 1,
                    "locale": locale,
                    "name": locale,
                    "development_only": development_only,
                }))
                (directory / "value.json").write_text(locale)
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
            paths = [value["path"] for value in first["inputs"]["files"]]
            self.assertIn("locales/en/value.json", paths)
            self.assertNotIn("locales/lorem/value.json", paths)
            self.assertNotIn("locales/README.md", paths)
            (root / "locales/lorem/value.json").write_text("changed fixture")
            subprocess.run(command, check=True)
            self.assertEqual(first, json.loads(output.read_text()))
            (root / "locales/en/value.json").write_text("changed")
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
