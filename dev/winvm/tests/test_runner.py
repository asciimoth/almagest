"""Run the production shell orchestration with fake QEMU and guest transports."""

import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import tempfile
import time
import unittest

ROOT = Path(__file__).resolve().parents[1]

FAKE = r'''#!/usr/bin/env python3
import io, json, os, pathlib, signal, sys, tarfile, time
name = pathlib.Path(sys.argv[0]).name
root = pathlib.Path(os.environ['FIXTURE'])
mode = os.environ['FAIL_STAGE']
if name == 'qemu-img':
    if 'create' in sys.argv:
        pathlib.Path(sys.argv[-1]).touch()
    else:
        print(json.dumps({'full-backing-filename': str(root / 'cache/images' / ('a'*64) / 'base.qcow2')}))
elif name == 'qemu-system-x86_64':
    (root / 'pid').write_text(str(os.getpid()))
    count = int((root / 'boots').read_text()) if (root / 'boots').exists() else 0
    (root / 'boots').write_text(str(count + 1))
    time.sleep(120)
elif name == 'qga.py':
    if any(arg.replace('\\', '/').endswith('/test-suite.ps1') for arg in sys.argv):
        (root / 'test-started').touch()
        with (root / 'test-launches').open('a') as out: out.write('privileged\n')
    if mode in ('freeze', 'lost-transport') and 'cmd.exe' in sys.argv and (root / 'boots').read_text() == '1':
        sys.exit(1)
    if mode == 'stuck-diagnostics' and any('collect-diagnostics.ps1' in arg for arg in sys.argv):
        time.sleep(120)
    if 'C:\\winvm\\ready' in sys.argv[-1]:
        if mode == 'boot': sys.exit(1)
        print('ready-token')
    if mode == 'interrupt' and 'exec' in sys.argv and (root / 'pid').exists():
        try:
            os.kill(int((root / 'pid').read_text()), 0)
            (root / 'diagnostics-before-shutdown').touch()
        except ProcessLookupError:
            pass
    if 'shutdown' in sys.argv and mode != 'shutdown' and (root / 'pid').exists():
        os.kill(int((root / 'pid').read_text()), signal.SIGTERM)
elif name == 'ssh':
    if 'test-suite.ps1' in sys.argv[-1]:
        (root / 'test-started').touch()
        with (root / 'test-launches').open('a') as out: out.write('ordinary\n')
    if mode in ('freeze', 'lost-transport') and 'cmd.exe /c exit 0' in sys.argv[-1] and (root / 'boots').read_text() == '1':
        sys.exit(1)
    if mode == 'lost-transport' and '-Mode ordinary' in sys.argv[-1]:
        sys.exit(255)
    if mode in ('freeze', 'healthy-silent', 'interrupt-tests') and '-Mode ordinary' in sys.argv[-1]:
        print('test output before interruption', flush=True)
        time.sleep(120)
    if mode in ('native-failure', 'guest-timeout', 'stuck-diagnostics') and '-Mode ordinary' in sys.argv[-1]:
        if mode == 'guest-timeout': print('panic: test timed out after 1s')
        print('[ordinary] FAIL fixture-case')
        sys.exit(1)
    if mode == 'interrupt':
        (root / 'ssh-started').touch()
        time.sleep(120)
    if mode == 'transfer' and 'New-Item' in sys.argv[-1]:
        sys.exit(1)
    if 'tar.exe -cf -' in sys.argv[-1]:
        if mode == 'artifacts':
            sys.exit(1)
        with tarfile.open(fileobj=sys.stdout.buffer, mode='w|') as archive:
            if os.environ.get('RUN_KIND') == 'resource':
                prefix = 'artifacts/windows-m8-resource/'
                native = {'schemaVersion':1, 'adapters':[], 'addresses':[], 'routes':[], 'dns':[]}
                report = {'warmupCycles':10, 'batchCount':4, 'cyclesPerBatch':25,
                          'samples':[{'handles':20+i, 'goroutines':5, 'privateBytes':1000000}
                                     for i in range(5)]}
                cases = {name:'passed' for name in (
                    'backend-lifecycle-cancellation-soak', 'daemon-graph-and-forced-restart',
                    'fault-injection-recovery-contracts', 'fault-injection-split-recovery-contracts')}
                faults = [{'case':name, 'method':'fault-injection', 'retained':'owned state',
                           'recovery':'explicit retry'} for name in (
                    'dns-external-edit', 'adapter-name-reuse', 'busy-close-and-failed-reset',
                    'zombie-or-changed-driver')]
                suite = {'schemaVersion':1, 'suite':'windows-m8-resource', 'outcome':'passed',
                         'platformId':'windows-server-2022-amd64', 'qualifying':True,
                         'workload':{'warmupCycles':10, 'batchCount':4, 'cyclesPerBatch':25,
                                     'cancellationCycles':100, 'soakDuration':'30m',
                                     'diagnosticOverride':False},
                         'settlingSeconds':10,
                         'budgets':{'maximumHandleGrowth':8, 'maximumGoroutineGrowth':4,
                                    'maximumPrivateByteGrowth':67108864},
                         'cases':cases, 'failureInjection':faults,
                         'recoveryJournal':{'persistentCrossProcessSplitJournal':False,
                                            'manualRecovery':'preserve evidence'}}
                for filename, value in (('suite.json', suite), ('resources-before.json', native),
                                        ('resources-after.json', native),
                                        ('process-resources.json', report)):
                    data = json.dumps(value).encode()
                    entry = tarfile.TarInfo(prefix + filename); entry.size = len(data)
                    archive.addfile(entry, io.BytesIO(data))
            elif os.environ.get('RUN_KIND') == 'native':
                suites = (('windows-m4-ordinary', False), ('windows-m4-privileged', True))
                if os.environ.get('RUN_MODE') == 'ordinary': suites = suites[:1]
                if os.environ.get('RUN_MODE') == 'privileged': suites = suites[1:]
                for suite, elevated in suites:
                    value = {'schemaVersion':1, 'platformId':'windows-server-2022-amd64',
                             'os':'windows-server-2022', 'architecture':'amd64',
                             'nativeExecution':True, 'suite':suite, 'outcome':'passed',
                             'identity':{'elevated':elevated},
                             'serialized':suite.endswith('privileged'),
                             'cases':{'required':'passed'}}
                    data = json.dumps(value).encode()
                    entry = tarfile.TarInfo(f'artifacts/{suite}/suite.json'); entry.size = len(data)
                    archive.addfile(entry, io.BytesIO(data))
                    coverage = b'mode: atomic\ngithub.com/asciimoth/almagest/lib/example.go:1.1,2.2 1 1\n'
                    entry = tarfile.TarInfo(f'artifacts/{suite}/coverage/example.coverage.out')
                    entry.size = len(coverage)
                    archive.addfile(entry, io.BytesIO(coverage))
            else:
                data = json.dumps({'suite':'harness-smoke','outcome':'passed','testExecution':'not-run'}).encode()
                entry = tarfile.TarInfo('artifacts/smoke.json'); entry.size = len(data)
                archive.addfile(entry, io.BytesIO(data))
elif name == 'package-worktree.py':
    pathlib.Path(sys.argv[1]).write_bytes(b'source fixture')
elif name == 'stage.sh':
    run = pathlib.Path(sys.argv[1])
    (run / 'payload.tar').write_bytes(b'payload fixture')
    (run / 'generated.sha256').write_text('fixture')
'''


class RunnerTests(unittest.TestCase):
    def test_success_and_failure_paths(self):
        for mode in ('success', 'native', 'ordinary', 'privileged', 'resource', 'native-failure', 'prepared', 'boot', 'transfer', 'artifacts', 'shutdown', 'interrupt',
                     'freeze', 'lost-transport', 'healthy-silent', 'guest-timeout', 'stuck-diagnostics', 'interrupt-tests'):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                scripts = root / 'dev/winvm'
                (scripts / 'tools').mkdir(parents=True)
                shutil.copy2(ROOT / 'run.sh', scripts / 'run.sh')
                shutil.copy2(ROOT / 'combine-coverage.py', scripts / 'combine-coverage.py')
                shutil.copy2(ROOT / 'supervise.py', scripts / 'supervise.py')
                shutil.copy2(ROOT / 'tools/resource-evidence.py', scripts / 'tools/resource-evidence.py')
                common = (ROOT / 'common.sh').read_text()
                common += '''
require_kvm() { :; }
base_key() { printf '%064d' 0 | tr 0 a; }
cached_image_valid() { :; }
find_ovmf() { echo "$FIXTURE/firmware"; }
wait_ready() {
    for _ in {1..100}; do [[ -f $FIXTURE/pid ]] && break; sleep .01; done
    [[ $FAIL_STAGE != boot ]] || die 'guest readiness timeout'
}
wait_for_pid() { [[ $FAIL_STAGE != shutdown ]]; }
'''
                # Use the real reaper; the shutdown fault only fails the grace wait.
                common += '''
stop_and_reap_pid() { kill -TERM "$1" 2>/dev/null || true; wait "$1" 2>/dev/null || true; }
'''
                (scripts / 'common.sh').write_text(common)
                config = json.loads((ROOT / 'config.json').read_text())
                config['machine'].update(bootTimeoutSeconds=1 if mode == 'boot' else 5,
                                         recoveryBootTimeoutSeconds=2, shutdownTimeoutSeconds=0.1,
                                         cleanupTimeoutSeconds=5,
                                         testTimeoutSeconds=1 if mode == 'healthy-silent' else 10,
                                         healthTimeoutSeconds=0.3, healthIntervalSeconds=0.1,
                                         artifactIntervalSeconds=0.2)
                (scripts / 'config.json').write_text(json.dumps(config))
                (root / 'firmware').touch()
                image = root / 'cache/images' / ('a' * 64)
                image.mkdir(parents=True)
                for name, data in {'base.qcow2': '', 'OVMF_VARS.fd': 'vars',
                                   'manifest.json': '{}', 'host-key.pub': 'ssh-ed25519 fixture\n'}.items():
                    (image / name).write_text(data)
                fakebin = root / 'bin'
                fakebin.mkdir()
                for path in [fakebin / n for n in ('qemu-img', 'qemu-system-x86_64', 'ssh', 'scp')] + [
                        scripts / 'tools/qga.py', scripts / 'stage.sh', scripts / 'package-worktree.py']:
                    path.write_text(FAKE)
                    path.chmod(0o755)
                native = mode in ('native', 'ordinary', 'privileged', 'native-failure', 'prepared', 'freeze', 'lost-transport', 'healthy-silent',
                                  'guest-timeout', 'stuck-diagnostics', 'interrupt-tests')
                run_kind = 'resource' if mode == 'resource' else 'native' if native else 'smoke'
                env = dict(os.environ, FIXTURE=temp, FAIL_STAGE=mode,
                           RUN_KIND=run_kind, RUN_MODE=mode,
                           WINVM_CACHE_DIR=str(root / 'cache'), WINVM_ENV_FILE=str(root / 'no-env'),
                           PATH=str(fakebin) + ':' + os.environ['PATH'])
                command = [scripts / 'run.sh'] + (['resource'] if mode == 'resource' else [mode] if mode in ('ordinary', 'privileged') else ['test'] if native else [])
                if mode == 'prepared':
                    prepared = root / 'prepared'
                    prepared.mkdir()
                    result = subprocess.run([scripts / 'run.sh', '--prepare', prepared],
                                            env=env, capture_output=True, text=True, timeout=15)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertFalse((root / 'pid').exists(), 'preparation must not boot QEMU')
                    # A prepared loop must not invoke either generator again.
                    (scripts / 'stage.sh').unlink()
                    (scripts / 'package-worktree.py').unlink()
                    env['WINVM_PREPARED_DIR'] = str(prepared)
                    run = root / 'artifacts/winvm/run-prepared'
                    run.mkdir()
                    env['WINVM_RUN_DIR'] = str(run)
                if mode in ('interrupt', 'interrupt-tests'):
                    process = subprocess.Popen(command, env=env, stdout=subprocess.PIPE,
                                               stderr=subprocess.PIPE, text=True,
                                               start_new_session=True)
                    ready_marker = root / ('test-started' if mode == 'interrupt-tests' else 'ssh-started')
                    for _ in range(500):
                        if ready_marker.exists():
                            break
                        time.sleep(0.01)
                    self.assertTrue(ready_marker.exists(),
                                    'foreground guest transport did not start')
                    qemu_pid = int((root / 'pid').read_text())
                    os.killpg(process.pid, signal.SIGINT)
                    try:
                        stdout, stderr = process.communicate(timeout=20)
                    except subprocess.TimeoutExpired:
                        process_state = subprocess.run(
                            ['ps', '-o', 'pid,ppid,pgid,stat,args', '--forest',
                             '-g', str(process.pid)], capture_output=True, text=True,
                            check=False).stdout
                        os.killpg(process.pid, signal.SIGKILL)
                        process.communicate()
                        self.fail('interrupted runner did not stop:\n' + process_state)
                    result = subprocess.CompletedProcess(command, process.returncode,
                                                         stdout, stderr)
                    if mode == 'interrupt':
                        self.assertTrue((root / 'diagnostics-before-shutdown').exists(),
                                        'Ctrl+C stopped QEMU before guest diagnostics')
                    with self.assertRaises(ProcessLookupError):
                        os.kill(qemu_pid, 0)
                else:
                    result = subprocess.run(command, env=env, capture_output=True,
                                            text=True, timeout=15)
                runs = list((root / 'artifacts/winvm').glob('run-*'))
                self.assertEqual(len(runs), 1, result.stderr)
                record = json.loads((runs[0] / 'run.json').read_text())
                staging = runs[0] / 'staging.log'
                diagnostic = result.stderr + (staging.read_text() if staging.exists() else '')
                passed = mode in ('success', 'native', 'ordinary', 'privileged', 'resource', 'prepared')
                self.assertEqual(record['status'], 'passed' if passed else 'failed', diagnostic)
                self.assertEqual(result.returncode == 0, passed, result.stderr)
                if mode in ('ordinary', 'privileged'):
                    self.assertEqual((root / 'test-launches').read_text(), mode + '\n')
                if not passed:
                    self.assertIn('Recovery: just winvm-shell', result.stderr)
                    expected_stage = 'boot' if mode == 'interrupt' else mode
                    if mode in ('native-failure', 'guest-timeout', 'stuck-diagnostics'):
                        expected_stage = 'tests'
                        self.assertTrue((runs[0] / 'guest-artifacts.tar').exists())
                        self.assertNotIn('running privileged tests', result.stdout)
                    if mode in ('freeze', 'lost-transport', 'healthy-silent', 'interrupt-tests'):
                        expected_stage = 'ordinary-tests'
                    if mode in ('freeze', 'lost-transport', 'healthy-silent', 'guest-timeout', 'interrupt-tests'):
                        self.assertTrue((runs[0] / 'guest-artifacts.tar').exists(), result.stdout)
                        self.assertEqual(record['recoveryAttempted'], mode in ('freeze', 'lost-transport'))
                        self.assertEqual(int((root / 'boots').read_text()), 2 if mode in ('freeze', 'lost-transport') else 1)
                        self.assertEqual((root / 'test-launches').read_text(), 'ordinary\n',
                                         'recovery must not retry tests')
                    if mode in ('freeze', 'lost-transport'):
                        self.assertEqual(record['failureKind'], 'guest-unresponsive')
                        self.assertTrue(record['finalArtifactsCollected'])
                        self.assertNotIn('running privileged tests', result.stdout)
                    if mode == 'healthy-silent':
                        self.assertEqual(record['failureKind'], 'host-test-timeout')
                        self.assertNotIn('running privileged tests', result.stdout)
                    if mode == 'guest-timeout':
                        self.assertEqual(record['failureKind'], 'tests')
                        self.assertIn('panic: test timed out', (runs[0] / 'ordinary-tests.log').read_text())
                    self.assertEqual(record['stage'], expected_stage)
                    self.assertTrue((runs[0] / 'diagnostics.log').exists() or
                                    mode == 'shutdown')
                    if mode in ('interrupt', 'interrupt-tests'):
                        self.assertIn('reason=ctrl-c',
                                      (runs[0] / 'diagnostics.log').read_text())
                pid = int((root / 'pid').read_text())
                with self.assertRaises(ProcessLookupError):
                    os.kill(pid, 0)
                self.assertTrue((runs[0] / 'overlay.qcow2').exists())
                self.assertTrue((runs[0] / 'OVMF_VARS.fd').exists())
