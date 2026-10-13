"""Host-only harness tests. No Windows guest or KVM is started."""

import hashlib
import importlib.util
import io
import json
import os
import re
import socket
import subprocess
import tarfile
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('package', ROOT / 'package-worktree.py')
package = importlib.util.module_from_spec(spec)
spec.loader.exec_module(package)


class HarnessTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name)
        self.env = dict(os.environ, WINVM_CACHE_DIR=str(self.path / 'cache'),
                        WINVM_ENV_FILE=str(self.path / 'no-env'))

    def shell(self, command, ok=True):
        result = subprocess.run(['bash', '-c', 'source "$1/common.sh"; ' + command,
                                 'test', str(ROOT)], env=self.env, text=True,
                                capture_output=True, timeout=15, check=False)
        if ok:
            self.assertEqual(result.returncode, 0, result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0, result.stdout)
        return result

    def test_lock_valid(self):
        subprocess.run([ROOT / 'doctor.sh', '--validate'], env=self.env, check=True,
                       stdout=subprocess.DEVNULL)

    def test_storage_space_distinguishes_cached_and_cold_images(self):
        public = self.path / 'public.key'
        public.write_text('test public key')
        self.env['TEST_VM_PUBLIC_KEY'] = str(public)
        prefix = '''
source "$winvm_dir/storage-space.sh"
ssh_public_key() { printf '%s\\n' "$TEST_VM_PUBLIC_KEY"; }
base_key() { printf 'known-key\\n'; }
df() { printf 'Filesystem 1024-blocks Used Available Capacity Mounted\\n'; printf 'test 200000000 1 %s 1%% /test\\n' "$TEST_VM_FREE_KIB"; }
'''
        for cached, free_gib, succeeds, diagnostic in (
            (True, 10, True, '10 GiB'),
            (True, 9, False, 'VM test artifacts needs 10 GiB'),
            (False, 89, False, 'VM image build cache needs 90 GiB'),
            (False, 90, True, ''),
        ):
            with self.subTest(cached=cached, free_gib=free_gib):
                self.env['TEST_VM_FREE_KIB'] = str(free_gib * 1024 * 1024)
                script = prefix + f'cached_image_valid() {{ return {0 if cached else 1}; }}\ncheck_vm_storage_space'
                result = self.shell(script, ok=succeeds)
                self.assertIn(diagnostic, result.stdout + result.stderr)

    def test_cached_space_check_uses_artifact_filesystem(self):
        self.shell('''
source "$winvm_dir/storage-space.sh"
ssh_public_key() { printf '%s\\n' "$config_file"; }
base_key() { printf 'known-key\\n'; }
cached_image_valid() { return 0; }
require_vm_space() {
    [[ $1 == "$repo_root/artifacts" || $1 == "$repo_root" ]] || exit 23
    [[ $2 == 10 ]] || exit 24
}
check_vm_storage_space
''')

    def test_storage_space_rejects_unreadable_measurement(self):
        result = self.shell('''
source "$winvm_dir/storage-space.sh"
df() { printf 'invalid disk report\\n'; }
require_vm_space "$winvm_cache_dir" 10 test
''', ok=False)
        self.assertIn('cannot measure free space', result.stderr)

    def test_absent_media_and_wrong_hash(self):
        self.shell('require_kvm /no-such-kvm-device', ok=False)
        self.shell('input_path windows', ok=False)
        self.shell('verify_hash absent /no-such-winvm-media ' + '0' * 64, ok=False)
        self.shell('verify_hash wrong "$config_file" ' + '0' * 64, ok=False)

    def test_native_dependencies_come_from_development_environment(self):
        driver = self.path / 'driver'
        wintun = self.path / 'wintun'
        for name in ('mullvad-split-tunnel.sys', 'mullvad-split-tunnel.inf',
                     'mullvad-split-tunnel.cat'):
            path = driver / 'amd64' / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(name.encode())
        for architecture in ('amd64', 'arm64'):
            path = wintun / architecture / 'wintun.dll'
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(architecture.encode())
        self.env['MULLVAD_SPLIT_TUNNEL_DRIVER_DIR'] = str(driver)
        self.env['SYSNET_WINDOWS_WINTUN_DIR'] = str(wintun)
        result = self.shell(
            'prepare_native_dependencies; '
            'printf "%s\n%s\n" "$winvm_driver_dir" "$winvm_wintun_dir"')
        self.assertEqual(result.stdout.splitlines(),
                         [str(driver / 'amd64'), str(wintun)])
        del self.env['SYSNET_WINDOWS_WINTUN_DIR']
        result = self.shell('prepare_native_dependencies', ok=False)
        self.assertIn('SYSNET_WINDOWS_WINTUN_DIR', result.stderr)

    def test_archive_clean_dirty_and_exclusions(self):
        repo = self.path / 'repo'
        repo.mkdir()
        def git(*args):
            return subprocess.run(['git', '-C', repo, *args], check=True,
                                  stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        git('init', '-q')
        git('config', 'user.email', 'test@example.invalid')
        git('config', 'user.name', 'test')
        for name in ('source.go', 'deleted.go', 'dev/winvm/env', 'media.iso',
                     'secrets/password', 'test.key', 'artifacts/disk.qcow2',
                     'bin/almagestd/static/index.html', '.env.local',
                     '.secretsignore'):
            p = repo / name
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_text('initial')
        git('add', '.')
        git('commit', '-qm', 'fixture')
        first = self.path / 'first.tar'
        digest = package.package(repo, first)
        with tarfile.open(first) as archive:
            self.assertEqual(archive.getnames(), ['deleted.go', 'source.go'])
        second = self.path / 'second.tar'
        self.assertEqual(digest, package.package(repo, second))
        (repo / 'source.go').write_text('dirty')
        (repo / 'deleted.go').unlink()
        (repo / 'new file.go').write_text('new')
        self.assertNotEqual(digest, package.package(repo, second))
        with tarfile.open(second) as archive:
            self.assertEqual(archive.getnames(), ['new file.go', 'source.go'])
            self.assertEqual(archive.extractfile('source.go').read(), b'dirty')
        record = json.loads(second.with_suffix('.json').read_text())
        self.assertTrue(record['dirty'])
        self.assertEqual(record['sha256'], hashlib.sha256(second.read_bytes()).hexdigest())
        certificate = repo / min(package.PUBLIC_CERTIFICATES)
        certificate.parent.mkdir(parents=True)
        certificate.write_text('-----BEGIN CERTIFICATE-----\npublic fixture')
        package.package(repo, second)
        with tarfile.open(second) as archive:
            self.assertIn(certificate.relative_to(repo).as_posix(), archive.getnames())
        certificate.write_text('-----BEGIN PRIVATE KEY-----')
        with self.assertRaisesRegex(ValueError, 'public certificate'):
            package.package(repo, second)
        certificate.unlink()
        (repo / 'escape').symlink_to('/etc/passwd')
        with self.assertRaisesRegex(ValueError, 'symlink'):
            package.package(repo, second)

    def test_provision_timeout_and_reboot_readiness(self):
        result = self.shell('ready_token() { return 1; }; wait_ready $$ 1 none /dev/null', ok=False)
        self.assertIn('readiness timeout', result.stderr)
        # A,B,B must need three probes. A,A would accept on the second probe.
        counter = self.path / 'count'
        counter.write_text('0')
        self.env['COUNTER'] = str(counter)
        self.shell('ready_token() { n=$(<"$COUNTER"); n=$((n+1)); echo "$n" >"$COUNTER"; if ((n==1)); then echo boot-A; else echo boot-B; fi; }; sleep() { :; }; wait_ready $$ 3 none /dev/null')
        self.assertEqual(counter.read_text().strip(), '3')

    def test_provisioning_starts_only_after_setup_reboot_and_reports_failure(self):
        provision = (ROOT / 'provision.ps1').read_text()
        builder = (ROOT / 'build-image.sh').read_text()
        self.assertIn("New-ScheduledTaskTrigger -AtStartup", provision)
        self.assertNotIn("Start-ScheduledTask -TaskName $taskName", provision)
        self.assertIn("provision.failed", provision)
        self.assertIn("provision.failed", builder)
        self.assertIn("Windows provisioning failed", builder)
        self.assertIn("Join-Path $media 'wintun-amd64.dll'", provision)
        self.assertIn("$driverSource = $media", provision)

    def test_native_runner_keeps_go_stderr_out_of_json_events(self):
        runner = (ROOT / 'test-suite.ps1').read_text()
        self.assertIn('RedirectStandardOutput = $eventPath', runner)
        self.assertIn('RedirectStandardError = $stderrPath', runner)
        self.assertNotIn('& go @testArguments 2>&1', runner)
        self.assertIn('$process.WaitForExit($progressIntervalSeconds * 1000)', runner)
        self.assertIn('$processHandle = $process.Handle', runner)
        self.assertNotIn('taskkill.exe', runner)
        self.assertNotIn('hardTimeoutSeconds', runner)
        self.assertIn('compilation has no deadline', runner)
        self.assertIn('Write-CaseState $statePath', runner)
        self.assertIn('goPackageTimeoutSeconds', runner)
        self.assertIn('$env:SYSNET_WINDOWS_WINTUN_DLL = $wintunDLL', runner)
        self.assertIn("Write-TestProgress 'RUN '", runner)
        self.assertIn("Write-TestProgress 'SUITE'", runner)
        self.assertIn('$caseProgress = "[$caseIndex/$caseCount] $($case.id)"', runner)
        self.assertIn("Write-TestProgress 'VALIDATE'", runner)
        self.assertIn("'PASS' } else { 'FAIL'", runner)
        self.assertIn('[switch]$ContinueAfterFailure', runner)
        self.assertIn('$processExitCode -ne 0 -and -not $ContinueAfterFailure', runner)
        self.assertIn("[string]$CaseIDs = ''", runner)
        self.assertIn('$CaseID = @()\nif ($CaseIDs)', runner)
        self.assertIn("@($CaseIDs -split '~')", runner)
        self.assertIn("$_.kind -ne 'go' -or $_.id -in $CaseID", runner)
        self.assertIn('$validationManifestPath', runner)
        self.assertIn('$collectCoverage = $true', runner)
        self.assertIn('if ($collectCoverage)', runner)
        self.assertIn('if ($collectCoverage -and -not (Test-Path', runner)

        manifest = json.loads((ROOT / 'test-suites.json').read_text())
        self.assertNotIn('caseTimeoutSeconds', manifest)
        no_coverage = []
        for suite in manifest['suites']:
            for case in suite['cases']:
                self.assertNotIn('timeoutSeconds', case)
                if case.get('coverage') is False:
                    no_coverage.append(case['id'])
        self.assertEqual(no_coverage, ['m32::windows-lane-fuzz'])

        supervisor = (ROOT / 'supervise.py').read_text()
        config = json.loads((ROOT / 'config.json').read_text())['machine']
        self.assertNotIn('testTimeoutSeconds', config)
        self.assertIn('if test_timeout is not None else float("inf")', supervisor)
        self.assertIn('str(0 if seconds is None else seconds)', supervisor)
        self.assertIn('os.environ.get("WINVM_FAIL_FAST", "1")', supervisor)
        self.assertIn('args.append("-ContinueAfterFailure")', supervisor)
        self.assertIn('os.environ.get("WINVM_TEST_CASES", "")', supervisor)
        self.assertIn('args += ["-CaseIDs", selected_cases.replace(",", "~")]', supervisor)

    def test_regular_winvm_gate_requires_isolated_packet_flow(self):
        justfile = ROOT.parents[1] / 'justfile'
        recipe = justfile.read_text()
        supervisor = (ROOT.parents[1] / 'scripts/test-e2e-fail-fast.sh').read_text()
        flow = (ROOT / 'flow-run.sh').read_text()
        runner = (ROOT / 'run.sh').read_text()
        vm_supervisor = (ROOT / 'supervise.py').read_text()
        self.assertIn('./dev/winvm/run.sh test\n  ./dev/winvm/flow-run.sh', recipe)
        self.assertIn('filter-dump,id=tunnel-capture', flow)
        self.assertIn('filter-dump,id=underlay-capture', flow)
        self.assertIn('flow-pcap.py', flow)
        self.assertIn('-scenario outnet', flow)
        self.assertIn('-scenario split', flow)
        self.assertEqual(flow.count('allocate_flow_port'), 3)
        self.assertEqual(runner.count('allocate_run_port'), 2)
        self.assertEqual(flow.count('WarnWeakCrypto=no'), 4)
        self.assertIn('>"$run_dir/flow-console.log" 2>&1', flow)
        self.assertIn("progress 'waiting for the client guest'", flow)
        self.assertIn('network_command_timeout=120', flow)
        self.assertEqual(flow.count('--timeout "$network_command_timeout"'), 2)
        self.assertIn("progress 'running outnet and split packet-flow scenarios (timeout: 30 minutes)'", flow)
        self.assertIn('packet-flow scenarios still running; elapsed=%ss', flow)
        self.assertIn("progress 'collecting observations and validating packet captures'", flow)
        self.assertIn('check *selectors: tidy typos fmt lint shellcheck vet # vulncheck', recipe)
        self.assertIn('just test-total {{quote(selectors)}}', recipe)
        self.assertIn('test-total-all: test-cycles-harness test-winvm-harness', recipe)
        self.assertIn('./scripts/test-e2e-fail-fast.sh test test-e2e-docker test-winvm-enabled', recipe)
        self.assertIn('test-winvm-harness:\n  @if [[ "${RUN_WINVM_TESTS:-0}" == "1" ]]', recipe)
        self.assertIn('lint-winvm:\n  @if [[ "${RUN_WINVM_TESTS:-0}" == "1" ]]', recipe)
        self.assertIn("shellcheck:\n  @if [[ \"${RUN_WINVM_TESTS:-0}\" == \"1\" ]]", recipe)
        self.assertIn("':!dev/winvm/**'", recipe)
        self.assertIn('test-e2e:\n  ./scripts/test-e2e-fail-fast.sh', recipe)
        self.assertIn('parallel_types="${TEST_TYPES_PARALLEL:-0}"', supervisor)
        self.assertIn('recipes=(test-e2e-docker test-winvm-enabled)', supervisor)
        self.assertIn('--output "$run_dir/host-resources.jsonl"', flow)
        self.assertIn('tools/host-resources.py', vm_supervisor)
        self.assertIn('RUN_WINVM_TESTS', recipe)

    def test_vm_host_lock_is_independent_of_cache_path(self):
        runtime = self.path / 'runtime'
        runtime.mkdir()
        self.env['XDG_RUNTIME_DIR'] = str(runtime)
        first = self.shell('winvm_host_lock_path').stdout.strip()
        first_marker = self.shell('qemu_orphan_marker').stdout.strip()
        self.env['WINVM_CACHE_DIR'] = str(self.path / 'other-cache')
        second = self.shell('winvm_host_lock_path').stdout.strip()
        second_marker = self.shell('qemu_orphan_marker').stdout.strip()
        self.assertEqual(first, second)
        self.assertEqual(first_marker, second_marker)
        self.assertEqual(first, str(runtime / 'almagest-winvm-host.lock'))
        for script in ('run.sh', 'flow-run.sh', 'build-image.sh'):
            self.assertIn('acquire_vm_host_lock', (ROOT / script).read_text())

        holder = subprocess.Popen(
            ['bash', '-c', 'source "$1/common.sh"; acquire_vm_host_lock; echo locked; read -r _',
             'test', str(ROOT)], env=self.env, text=True, stdin=subprocess.PIPE,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        try:
            self.assertEqual(holder.stdout.readline().strip(), 'locked')
            other_env = dict(self.env, WINVM_CACHE_DIR=str(self.path / 'third-cache'))
            contender = subprocess.run(
                ['timeout', '0.2', 'bash', '-c',
                 'source "$1/common.sh"; acquire_vm_host_lock', 'test', str(ROOT)],
                env=other_env, capture_output=True, text=True, check=False)
            self.assertEqual(contender.returncode, 124, contender.stderr)
        finally:
            holder.terminate()
            holder.wait(timeout=3)
            holder.stdin.close()
            holder.stdout.close()
            holder.stderr.close()

    def test_regular_manifest_covers_windows_go_and_integration_tests(self):
        manifest = json.loads((ROOT / 'test-suites.json').read_text())
        ordinary = next(suite for suite in manifest['suites']
                        if suite['id'] == 'windows-m4-ordinary')
        full_modules = {case.get('module') for case in ordinary['cases']
                        if case.get('kind') == 'go' and case.get('arguments') == ['./...']}
        self.assertEqual(full_modules, {'certcat', 'lib', 'bin'})

        required = {
            test.rsplit('::', 1)[-1]
            for suite in manifest['suites']
            for case in suite['cases']
            for test in case.get('requiredTests', [])
            if '/bin/integration::' in test
        }
        integration = ROOT.parents[1] / 'bin/integration'
        declared = set()
        for path in integration.glob('*_windows_test.go'):
            source = path.read_text()
            if 'winintegration' not in source.split('\n', 1)[0]:
                continue
            declared.update(re.findall(r'^func (Test\w+)\(t \*testing\.T\)',
                                       source, re.MULTILINE))

        # These subprocess entry points are invoked by other tests. The two
        # resource cases belong only to the fixed qualification suite.
        non_regular = {
            'TestWindowsDaemonCommandHelper',
            'TestWindowsBatch2CommandHelper',
            'TestWindowsDaemonNativeRepeatedGraphChanges',
            'TestWindowsDaemonNativeForcedTerminationAndRestart',
        }
        self.assertEqual(declared - non_regular, required)

    def test_manual_daemon_is_elevated_and_keeps_web_local(self):
        runner = (ROOT / 'supervise.py').read_text()
        launcher = (ROOT / 'manual-daemon.ps1').read_text()
        shell = (ROOT / 'run.sh').read_text()
        self.assertIn('self.qga("exec", *arguments', runner)
        self.assertIn('127.0.0.1:{self.args.web_port}:127.0.0.1:8090', runner)
        self.assertIn('Invoke-CimMethod -ClassName Win32_Process', launcher)
        self.assertIn('GOOS=windows GOARCH=amd64 CGO_ENABLED=0',
                      (ROOT / 'stage.sh').read_text())
        self.assertIn('almagest-cli.exe ./almagest-cli',
                      (ROOT / 'stage.sh').read_text())
        self.assertIn("$toolDir = 'C:\\winvm\\bin'", launcher)
        self.assertIn("'winvm:(OI)(CI)RX'", launcher)
        self.assertIn("'-w', [string]$WebPort", launcher)
        self.assertIn("'--file-storage', $storage", launcher)
        self.assertNotIn("'--tmp-file-storage'", launcher)
        self.assertIn("[string]$DaemonOptions = ''", launcher)
        self.assertIn('os.environ.get("WINVM_DAEMON_OPTIONS", "")', runner)
        self.assertIn('display=(-display "${WINVM_DISPLAY:-gtk,window-close=off}"', shell)
        self.assertIn('enabling automatic login for the standard winvm account', runner)
        self.assertIn('self.upload_manual_state()', runner)
        self.assertIn('self.export_manual_state()', runner)
        self.assertIn('local/winvm', shell)

    def test_interrupt_diagnostics_are_extensive(self):
        runner = (ROOT / 'supervise.py').read_text()
        collector = (ROOT / 'collect-diagnostics.ps1').read_text()
        self.assertIn('ctrl-c', runner)
        self.assertIn('query-cpus-fast', runner)
        self.assertIn('collect-diagnostics.ps1', runner)
        self.assertIn('start_new_session=True', runner)
        self.assertIn('Win32_Process', collector)
        self.assertIn('go-processes.txt', collector)
        self.assertIn('system.evtx', collector)
        self.assertIn('active-test-tails.txt', collector)
        self.assertIn('StartsWith($diagnosticRoot', collector)

    def test_resource_suite_uses_native_exit_code(self):
        suite = (ROOT / 'resource-suite.ps1').read_text()
        self.assertIn("$ErrorActionPreference = 'Continue'", suite)
        self.assertIn('$goExitCode = $LASTEXITCODE', suite)
        self.assertIn('$ErrorActionPreference = $savedErrorActionPreference', suite)
        self.assertIn('if ($goExitCode -ne 0)', suite)
        self.assertIn("$_.PSObject.Properties['Test']", suite)

    def test_port_contention_and_concurrent_locks(self):
        sock = socket.socket()
        self.addCleanup(sock.close)
        sock.bind(('127.0.0.1', 0))
        port = sock.getsockname()[1]
        config = self.path / 'config.json'
        config.write_text(json.dumps({'ssh': {'portMinimum': port, 'portMaximum': port}}))
        self.env['TEST_CONFIG'] = str(config)
        result = self.shell('config_file=$TEST_CONFIG; allocate_locked_port', ok=False)
        self.assertNotIn('Traceback', result.stderr)
        self.assertIn('no free locked SSH port is available', result.stderr)
        sock.close()
        self.shell('config_file=$TEST_CONFIG; allocate_locked_port; (allocate_locked_port)', ok=False)

    def test_run_paths_are_isolated(self):
        one = self.shell('make_socket_dir').stdout.strip()
        two = self.shell('make_socket_dir').stdout.strip()
        try:
            self.assertNotEqual(one, two)
            self.assertLess(len(one), 98)
        finally:
            Path(one).rmdir()
            Path(two).rmdir()

    def test_shutdown_is_bounded(self):
        self.shell("sleep 60 & p=$!; stop_and_reap_pid $p 0 1 1; ! kill -0 $p 2>/dev/null")
        # Never call an unbounded wait if even SIGKILL did not stop QEMU.
        result = self.shell('wait_for_pid() { return 1; }; kill() { :; }; wait() { echo UNBOUNDED; }; stop_and_reap_pid 42 0 0 0', ok=False)
        self.assertNotIn('UNBOUNDED', result.stdout)

    def test_failed_artifact_retrieval(self):
        self.shell('validate_smoke_artifact /no-such-artifact', ok=False)
        archive = self.path / 'artifact.tar'
        self.env['ARTIFACT'] = str(archive)
        for outcome in ('failed', 'passed'):
            data = json.dumps({'suite': 'harness-smoke', 'outcome': outcome,
                               'testExecution': 'not-run'}).encode('utf-8-sig')
            with tarfile.open(archive, 'w') as tar:
                entry = tarfile.TarInfo('artifacts/smoke.json')
                entry.size = len(data)
                tar.addfile(entry, io.BytesIO(data))
            self.shell('validate_smoke_artifact "$ARTIFACT"', ok=outcome == 'passed')

    def test_native_artifacts_require_both_identities(self):
        archive = self.path / 'native.tar'
        self.env['ARTIFACT'] = str(archive)
        def suite(name, elevated):
            return json.dumps({
                'schemaVersion': 1, 'platformId': 'windows-server-2022-amd64',
                'os': 'windows-server-2022', 'architecture': 'amd64',
                'nativeExecution': True, 'suite': name,
                'identity': {'elevated': elevated},
                'serialized': name.endswith('privileged'), 'outcome': 'passed',
                'cases': {'required': 'passed'},
            }).encode()
        with tarfile.open(archive, 'w') as tar:
            for name, elevated in (('windows-m4-ordinary', False), ('windows-m4-privileged', True)):
                data = suite(name, elevated)
                entry = tarfile.TarInfo(f'artifacts/{name}/suite.json')
                entry.size = len(data)
                tar.addfile(entry, io.BytesIO(data))
        self.shell('validate_native_test_artifact "$ARTIFACT"')
        self.shell('validate_native_test_artifact "$ARTIFACT" ordinary')
        self.shell('validate_native_test_artifact "$ARTIFACT" privileged')
        with tarfile.open(archive, 'w') as tar:
            data = suite('windows-m4-ordinary', False)
            entry = tarfile.TarInfo('artifacts/windows-m4-ordinary/suite.json')
            entry.size = len(data)
            tar.addfile(entry, io.BytesIO(data))
        self.shell('validate_native_test_artifact "$ARTIFACT"', ok=False)
        self.shell('validate_native_test_artifact "$ARTIFACT" ordinary')
        self.shell('validate_native_test_artifact "$ARTIFACT" privileged', ok=False)

    def test_native_coverage_profiles_are_combined(self):
        archive = self.path / 'native-coverage.tar'
        output = self.path / 'coverage'
        self.env.update(ARTIFACT=str(archive), OUTPUT=str(output))
        profiles = {
            'artifacts/ordinary/coverage/lib.out': (
                'mode: atomic\n'
                'github.com/asciimoth/almagest/lib/example.go:1.1,2.2 2 1\n'
                'github.com/asciimoth/almagest/lib/example.go:3.1,4.2 1 0\n'
            ),
            'artifacts/privileged/coverage/lib.out': (
                'mode: atomic\n'
                'github.com/asciimoth/almagest/lib/example.go:1.1,2.2 2 3\n'
                'github.com/asciimoth/sysnet-windows/integration/example.go:1.1,2.2 5 1\n'
            ),
        }
        with tarfile.open(archive, 'w') as tar:
            for name, value in profiles.items():
                data = value.encode()
                entry = tarfile.TarInfo(name)
                entry.size = len(data)
                tar.addfile(entry, io.BytesIO(data))
        subprocess.run(
            ['python3', ROOT / 'combine-coverage.py', archive, output], check=True,
            text=True, capture_output=True,
        )
        self.assertEqual(
            (output / 'coverage.out').read_text(),
            'mode: atomic\n'
            'github.com/asciimoth/almagest/lib/example.go:1.1,2.2 2 4\n'
            'github.com/asciimoth/almagest/lib/example.go:3.1,4.2 1 0\n',
        )
        self.assertIn('66.7%', (output / 'total.txt').read_text())


if __name__ == '__main__':
    unittest.main()
