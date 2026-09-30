#!/usr/bin/env python3
"""Package current Git files, with the same exclusions for clean and dirty trees."""

import argparse
import hashlib
import io
import json
from pathlib import Path, PurePosixPath
import subprocess
import tarfile

# Public test authorities are source inputs, not local credentials.
PUBLIC_CERTIFICATES = {
    'certcat/data/examples/narrow-scope-ca.pem',
    'certcat/data/examples/scoped-service-anchor.pem',
    'certcat/data/examples/warning-interception-root.pem',
    'e2e/cases/tls_os_reload/fixtures/narrow-scope-ca.pem',
    'e2e/cases/tls_os_reload/fixtures/warning-interception-root.pem',
}


def eligible(name):
    path = PurePosixPath(name)
    parts = tuple(p.lower() for p in path.parts)
    blocked = {'.git', '.direnv', 'node_modules', 'artifacts', '.artifacts',
               'coverage', 'local', 'secrets', 'private', '__pycache__'}
    if blocked.intersection(parts):
        return False
    if any(p == 'env' or p == '.env' or p.startswith('.env.') and p != '.env.example'
           for p in parts):
        return False
    if name not in PUBLIC_CERTIFICATES and path.suffix.lower() in {'.iso', '.qcow2', '.vhd', '.vhdx', '.vmdk', '.img',
                               '.fd', '.sock', '.pem', '.key', '.pfx', '.p12'}:
        return False
    if parts[-1] in {'id_rsa', 'id_ed25519', 'go.work', 'go.work.sum', '.envrc'}:
        return False
    return not (name.startswith(('bin/almagestd/static/', 'panel/public/', 'panel/vendor/'))
                or name == 'certcat/catalog_gen.go')


def package(root, output):
    names = subprocess.check_output(['git', '-C', str(root), 'ls-files', '-z',
                                     '--cached', '--others', '--exclude-standard'])
    records = []
    with tarfile.open(output, 'w', format=tarfile.PAX_FORMAT) as archive:
        for name in sorted(set(names.decode().strip('\0').split('\0'))):
            if not name or not eligible(name):
                continue
            path = root / name
            if path.resolve() == output.resolve():
                continue
            # Windows extraction must not follow source-tree links out of staging.
            if path.is_symlink() or not path.resolve().is_relative_to(root.resolve()):
                raise ValueError(f'source symlink is not supported: {name}')
            if not path.exists():
                continue  # Deleted tracked file.
            if not path.is_file():
                raise ValueError(f'source is not a regular file: {name}')
            data = path.read_bytes()
            if name in PUBLIC_CERTIFICATES and (b'PRIVATE KEY' in data or b'BEGIN CERTIFICATE' not in data):
                raise ValueError(f'public certificate fixture contains unexpected data: {name}')
            entry = tarfile.TarInfo(name)
            entry.size = len(data)
            entry.mode = 0o755 if path.stat().st_mode & 0o111 else 0o644
            archive.addfile(entry, io.BytesIO(data))
            records.append({'path': name, 'sha256': hashlib.sha256(data).hexdigest()})
    digest = hashlib.sha256(output.read_bytes()).hexdigest()
    output.with_suffix('.json').write_text(json.dumps({
        'sha256': digest, 'files': records,
        'revision': subprocess.check_output(['git', '-C', str(root), 'rev-parse', 'HEAD']).decode().strip(),
        'dirty': bool(subprocess.check_output(['git', '-C', str(root), 'status', '--porcelain'])),
    }, indent=2) + '\n')
    return digest


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('output', type=Path)
    parser.add_argument('--root', type=Path, default=Path(__file__).resolve().parents[2])
    args = parser.parse_args()
    print(package(args.root, args.output))
