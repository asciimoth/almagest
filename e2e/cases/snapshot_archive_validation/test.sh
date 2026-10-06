#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

export ALMAGEST_E2E_TMP_FILE_STORAGE=0
export ALMAGEST_E2E_FILE_STORAGE=/var/lib/almagest-e2e-snapshot-archive-validation

assert_invalid_import() {
	local archive="$1"
	local expected="${2:-snapshot is invalid}"
	local name
	name="$(basename "$archive" .zip)"
	if cli snapshots import "$archive" >"$E2E_RUNTIME/$name.out" 2>"$E2E_RUNTIME/$name.err"; then
		fail "$name unexpectedly imported"
	fi
	grep -F -- "$expected" "$E2E_RUNTIME/$name.err" >/dev/null \
		|| fail "$name did not report: $expected"
}

start_daemon

cli snapshots create --name source >"$E2E_RUNTIME/created.json"
snapshot_uuid="$(jq -er '.uuid' "$E2E_RUNTIME/created.json")"
source_archive="$E2E_RUNTIME/source.zip"
cli snapshots export --output "$source_archive" "$snapshot_uuid" >/dev/null

python3 - "$source_archive" "$E2E_RUNTIME" <<'PY'
import copy
import json
import stat
import sys
import zipfile

source_path, output_dir = sys.argv[1:]

with zipfile.ZipFile(source_path) as source:
    entries = [(copy.copy(info), source.read(info)) for info in source.infolist()]

manifest_data = next(data for info, data in entries if info.filename == "manifest.json")
manifest = json.loads(manifest_data)
database_name = next(
    info.filename for info, _ in entries
    if info.filename == "files/kv.db"
)

def info_for(name, mode=stat.S_IFREG | 0o600):
    info = zipfile.ZipInfo(name)
    info.create_system = 3
    info.external_attr = mode << 16
    info.compress_type = zipfile.ZIP_DEFLATED
    return info

def write(name, transform=None, additions=()):
    target = f"{output_dir}/{name}.zip"
    current = [(copy.copy(info), data) for info, data in entries]
    if transform is not None:
        current = transform(current)
    with zipfile.ZipFile(target, "w", allowZip64=True) as archive:
        for info, data in current:
            archive.writestr(info, data)
        for info, data in additions:
            archive.writestr(info, data)

def without(filename):
    return lambda current: [(info, data) for info, data in current if info.filename != filename]

def replace(filename, data):
    return lambda current: [
        (info, data if info.filename == filename else old_data)
        for info, old_data in current
    ]

def replace_manifest(update):
    changed = copy.deepcopy(manifest)
    update(changed)
    return replace("manifest.json", json.dumps(changed).encode())

write("missing-manifest", without("manifest.json"))
write("missing-config", without("config.hujson"))
write("missing-database", without(database_name))
write("duplicate-config", additions=[(info_for("config.hujson"), b'{"nodes":{}}')])
write("unknown-entry", additions=[(info_for("unexpected.txt"), b"unexpected")])
write("absolute-entry", additions=[(info_for("/absolute"), b"unsafe")])
write("traversal-entry", additions=[(info_for("../escape"), b"unsafe")])
write("unclean-entry", additions=[(info_for("files/a/../b"), b"unsafe")])
write("backslash-entry", additions=[(info_for("files\\unsafe"), b"unsafe")])
write("invalid-manifest-json", replace("manifest.json", b"{"))
write("unsupported-version", replace_manifest(lambda value: value.update(version=2)))
write("invalid-timestamp", replace_manifest(lambda value: value["snapshot"].update(timestamp=0)))
write("invalid-uuid", replace_manifest(lambda value: value["snapshot"].update(uuid="invalid")))
write("invalid-config", replace("config.hujson", b"this is not hujson"))
write("invalid-database", replace(database_name, b"this is not bbolt"))
write(
    "unsupported-file-mode",
    additions=[(info_for("files/fifo", stat.S_IFIFO | 0o600), b"")],
)
write(
    "absolute-symlink",
    additions=[(info_for("files/absolute-link", stat.S_IFLNK | 0o777), b"/etc/passwd")],
)
write(
    "dangling-symlink",
    additions=[(info_for("files/dangling-link", stat.S_IFLNK | 0o777), b"missing")],
)
write(
    "escaping-symlink",
    additions=[(info_for("files/escaping-link", stat.S_IFLNK | 0o777), b"../../outside")],
)
write(
    "symlink-parent",
    additions=[
        (info_for("files/link-parent", stat.S_IFLNK | 0o777), b"."),
        (info_for("files/link-parent/child"), b"unsafe"),
    ],
)
write(
    "non-directory-parent",
    additions=[
        (info_for("files/plain-parent"), b"plain"),
        (info_for("files/plain-parent/child"), b"invalid"),
    ],
)
write(
    "oversized-manifest",
    replace("manifest.json", b" " * (64 * 1024 + 1)),
)
write(
    "oversized-config",
    replace("config.hujson", b" " * (8 * 1024 * 1024 + 1)),
)
write(
    "extracted-size-bomb",
    additions=[(info_for("files/size-bomb"), b"0" * (256 * 1024 * 1024 + 1))],
)
write(
    "startup-config-conflict",
    additions=[(info_for("files/config"), b"would replace active config directory")],
)
PY

for archive in \
	missing-manifest missing-config missing-database duplicate-config unknown-entry \
	absolute-entry traversal-entry unclean-entry backslash-entry invalid-manifest-json \
	unsupported-version invalid-timestamp invalid-uuid invalid-config invalid-database \
	unsupported-file-mode absolute-symlink dangling-symlink escaping-symlink \
	symlink-parent non-directory-parent; do
	assert_invalid_import "$E2E_RUNTIME/$archive.zip"
done
for archive in oversized-manifest oversized-config extracted-size-bomb; do
	assert_invalid_import "$E2E_RUNTIME/$archive.zip" "snapshot exceeds the size limit"
done

jq -e --arg uuid "$snapshot_uuid" '
	.snapshots | length == 1 and .[0].uuid == $uuid and .[0].name == "source"
' <(cli snapshots list) >/dev/null \
	|| fail "rejected imports changed the snapshot list"
cli admin ping --nonce archive-validation | jq -e '.nonce == "archive-validation"' >/dev/null \
	|| fail "daemon did not remain responsive after rejected imports"

# Force startup restore to fail after it has begun mirroring storage. The
# archive contains a regular file at the directory that holds the active
# config. The restore must reject that replacement and roll back files that it
# removed earlier in the mirror operation.
printf '%s\n' rollback-preserved >"$ALMAGEST_E2E_FILE_STORAGE/rollback-marker"
mkdir -p "$ALMAGEST_E2E_FILE_STORAGE/config"
cp "$E2E_CONFIG_DIR/alm.hujson" "$ALMAGEST_E2E_FILE_STORAGE/config/alm.hujson"
cp "$E2E_CONFIG_DIR/protected_alm.hujson" \
	"$ALMAGEST_E2E_FILE_STORAGE/config/protected_alm.hujson"
stop_daemon

failure_log="$E2E_RUNTIME/startup-rollback.log"
failure_socket="$E2E_RUNTIME/startup-rollback.sock"
XDG_RUNTIME_DIR="$E2E_RUNTIME" almagestd \
	--experimental \
	--no-isolation \
	-c "$ALMAGEST_E2E_FILE_STORAGE/config/alm.hujson" \
	-p "$ALMAGEST_E2E_FILE_STORAGE/config/protected_alm.hujson" \
	--unsafe-config \
	--file-storage "$ALMAGEST_E2E_FILE_STORAGE" \
	--snapshot "$E2E_RUNTIME/startup-config-conflict.zip" \
	--unix "$failure_socket" --web 0 -l debug \
	>"$failure_log" 2>&1 &
failure_pid="$!"
if wait "$failure_pid"; then
	fail "conflicting startup snapshot unexpectedly succeeded"
fi
grep -F "would replace a directory that contains an active config source" \
	"$failure_log" >/dev/null \
	|| fail "startup snapshot did not report its active-config conflict"
[[ "$(<"$ALMAGEST_E2E_FILE_STORAGE/rollback-marker")" == "rollback-preserved" ]] \
	|| fail "failed startup snapshot did not restore the prior storage files"
[[ -f "$ALMAGEST_E2E_FILE_STORAGE/config/alm.hujson" ]] \
	|| fail "failed startup snapshot removed the active config"

start_daemon
cli admin ping --nonce archive-rollback | jq -e '.nonce == "archive-rollback"' >/dev/null \
	|| fail "daemon did not restart after startup snapshot rollback"

log "case passed"
