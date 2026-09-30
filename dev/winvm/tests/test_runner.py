"""Run the production shell orchestration with fake QEMU and guest transports."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
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
    time.sleep(120)
elif name == 'qga.py':
    if 'shutdown' in sys.argv and (root / 'pid').exists():
        os.kill(int((root / 'pid').read_text()), signal.SIGTERM)
elif name == 'ssh':
    if mode == 'transfer' and 'New-Item' in sys.argv[-1]:
        sys.exit(1)
elif name == 'scp':
    if sys.argv[-1].endswith('guest-artifacts.tar'):
        if mode == 'artifacts':
            sys.exit(1)
        with tarfile.open(sys.argv[-1], 'w') as archive:
            if os.environ.get('RUN_KIND') == 'native':
                for suite, elevated in (('windows-m3-ordinary', False), ('windows-m3-privileged', True)):
                    value = {'schemaVersion':1, 'platformId':'windows-server-2022-amd64',
                             'os':'windows-server-2022', 'architecture':'amd64',
                             'nativeExecution':True, 'suite':suite, 'outcome':'passed',
                             'identity':{'elevated':elevated},
                             'serialized':suite.endswith('privileged'),
                             'cases':{'required':'passed'}}
                    data = json.dumps(value).encode()
                    entry = tarfile.TarInfo(f'artifacts/{suite}/suite.json'); entry.size = len(data)
                    archive.addfile(entry, io.BytesIO(data))
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
        for mode in ('success', 'native', 'boot', 'transfer', 'artifacts', 'shutdown'):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                scripts = root / 'dev/winvm'
                (scripts / 'tools').mkdir(parents=True)
                shutil.copy2(ROOT / 'run.sh', scripts / 'run.sh')
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
                shutil.copy2(ROOT / 'config.json', scripts / 'config.json')
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
                env = dict(os.environ, FIXTURE=temp, FAIL_STAGE=mode,
                           RUN_KIND='native' if mode == 'native' else 'smoke',
                           WINVM_CACHE_DIR=str(root / 'cache'), WINVM_ENV_FILE=str(root / 'no-env'),
                           PATH=str(fakebin) + ':' + os.environ['PATH'])
                command = [scripts / 'run.sh'] + (['test'] if mode == 'native' else [])
                result = subprocess.run(command, env=env, capture_output=True,
                                        text=True, timeout=15)
                runs = list((root / 'artifacts/winvm').glob('run-*'))
                self.assertEqual(len(runs), 1, result.stderr)
                record = json.loads((runs[0] / 'run.json').read_text())
                staging = runs[0] / 'staging.log'
                diagnostic = result.stderr + (staging.read_text() if staging.exists() else '')
                passed = mode in ('success', 'native')
                self.assertEqual(record['status'], 'passed' if passed else 'failed', diagnostic)
                self.assertEqual(result.returncode == 0, passed, result.stderr)
                if not passed:
                    self.assertIn('Recovery: just winvm-shell', result.stderr)
                    self.assertEqual(record['stage'], mode)
                    self.assertTrue((runs[0] / 'diagnostics.log').exists() or mode == 'shutdown')
                self.assertTrue((runs[0] / 'overlay.qcow2').exists())
                self.assertTrue((runs[0] / 'OVMF_VARS.fd').exists())
