"""Host-only harness tests. No Windows guest or KVM is started."""

import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import socket
import subprocess
import tarfile
import tempfile
import unittest

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
                                capture_output=True, timeout=15)
        if ok:
            self.assertEqual(result.returncode, 0, result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0, result.stdout)
        return result

    def test_lock_valid(self):
        subprocess.run([ROOT / 'doctor.sh', '--validate'], env=self.env, check=True,
                       stdout=subprocess.DEVNULL)

    def test_absent_media_and_wrong_hash(self):
        self.shell('require_kvm /no-such-kvm-device', ok=False)
        self.shell('input_path windows', ok=False)
        self.shell('verify_hash absent /no-such-winvm-media ' + '0' * 64, ok=False)
        self.shell('verify_hash wrong "$config_file" ' + '0' * 64, ok=False)

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
                     'bin/almagestd/static/index.html', '.env.local'):
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
        certificate = repo / sorted(package.PUBLIC_CERTIFICATES)[0]
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
        self.assertIn('$ErrorActionPreference = \'Continue\'', runner)
        self.assertIn('2> $stderrPath', runner)
        self.assertNotIn('& go @testArguments 2>&1', runner)
        self.assertIn('$processExitCode = $LASTEXITCODE', runner)
        self.assertIn('$env:SYSNET_WINDOWS_WINTUN_DLL = $wintunDLL', runner)
        self.assertIn("Write-TestProgress 'RUN '", runner)
        self.assertIn("'PASS' } else { 'FAIL'", runner)

    def test_port_contention_and_concurrent_locks(self):
        sock = socket.socket()
        self.addCleanup(sock.close)
        sock.bind(('127.0.0.1', 0))
        port = sock.getsockname()[1]
        config = self.path / 'config.json'
        config.write_text(json.dumps({'ssh': {'portMinimum': port, 'portMaximum': port}}))
        self.env['TEST_CONFIG'] = str(config)
        self.shell('config_file=$TEST_CONFIG; allocate_locked_port', ok=False)
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
        with tarfile.open(archive, 'w') as tar:
            data = suite('windows-m4-ordinary', False)
            entry = tarfile.TarInfo('artifacts/windows-m4-ordinary/suite.json')
            entry.size = len(data)
            tar.addfile(entry, io.BytesIO(data))
        self.shell('validate_native_test_artifact "$ARTIFACT"', ok=False)

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
