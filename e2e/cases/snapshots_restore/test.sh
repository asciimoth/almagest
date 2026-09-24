#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

export ALMAGEST_E2E_TMP_FILE_STORAGE=0
export ALMAGEST_E2E_FILE_STORAGE=/var/lib/almagest-e2e-snapshots

target_body="snapshot restore target"
target_log="$E2E_RUNTIME/snapshot-target.log"
target_pid=""

cleanup() {
	stop_static_http_target "$target_pid"
	stop_daemon
}

assert_socks_password() {
	local password="$1"
	local expected="$2"
	local output="$E2E_RUNTIME/socks-${password}.txt"
	if curl --fail --silent --show-error --max-time 4 \
		--socks5-hostname 127.0.0.1:11108 \
		--proxy-user "snapshot-user:${password}" \
		"http://127.0.0.1:18108/${password}" \
		--output "$output" 2>/dev/null; then
		[[ "$expected" == "success" ]] || fail "SOCKS accepted unexpected password $password"
		[[ "$(<"$output")" == "$target_body" ]] || fail "SOCKS returned unexpected body"
	else
		[[ "$expected" == "failure" ]] || fail "SOCKS rejected expected password $password"
	fi
}

curl_expected_socks_password() {
	local password="$1"
	local output="$E2E_RUNTIME/socks-${password}.txt"
	curl --fail --silent --show-error --max-time 4 \
		--socks5-hostname 127.0.0.1:11108 \
		--proxy-user "snapshot-user:${password}" \
		"http://127.0.0.1:18108/${password}" \
		--output "$output" \
		&& [[ "$(<"$output")" == "$target_body" ]]
}

target_pid="$(start_static_http_target 127.0.0.1 18108 "$target_body" "$target_log")"
start_daemon
trap cleanup EXIT

for name in "SOCKS Snapshot" "SOCKS Listen Network" "SOCKS Proxy Network" "Persistent Mock"; do
	wait_graph_node "$name" || fail "initial node $name is missing"
done
initial_socks_id="$(node_id_by_name "SOCKS Snapshot")"

cli secrets set --key snapshot-mutable --value before-secret >/tmp/snapshot-secret-set.json
retry "$ALMAGEST_E2E_TIMEOUT" "secret-backed SOCKS listener" \
	log_contains "almagest/SocksServer" "socks listening on 127.0.0.1:11108" \
	|| fail "SOCKS listener did not start"
retry "$ALMAGEST_E2E_TIMEOUT" "SOCKS request with initial snapshot password" \
	curl_expected_socks_password protected-socks-password \
	|| fail "SOCKS rejected the protected password"

cli kv set --key snapshot-default --data before-default >/tmp/snapshot-kv-default.json
cli kv set --plugin snapshot-plugin --path nested --key snapshot-plugin-key --data before-plugin >/tmp/snapshot-kv-plugin.json
dns_key="$(cli dns set --cache snapshot-cache --name snapshot.test. --type A --ttl 3600 --value 192.0.2.108 | jq -er '.key')"
builtin_fingerprint="$(cli tls builtin-ca | jq -er '.cert.fingerprint')"

mkdir -p "$ALMAGEST_E2E_FILE_STORAGE/future-storage/nested"
printf '%s\n' before-file >"$ALMAGEST_E2E_FILE_STORAGE/future-storage/nested/value.txt"
ln -s nested/value.txt "$ALMAGEST_E2E_FILE_STORAGE/future-storage/value-link"
ln -s value-link "$ALMAGEST_E2E_FILE_STORAGE/future-storage/value-link-chain"
protected_checksum="$(sha256sum "$E2E_CONFIG_DIR/protected_alm.hujson" | cut -d ' ' -f 1)"

cli snapshots create --name "complex daemon" >/tmp/snapshot-created.json
snapshot_uuid="$(jq -er '.uuid' /tmp/snapshot-created.json)"
exported_archive="$E2E_RUNTIME/$snapshot_uuid.zip"
jq -e --arg uuid "$snapshot_uuid" --arg name "complex daemon" \
	'.snapshots | length == 1 and .[0].uuid == $uuid and .[0].name == $name and (.[0].timestamp > 0)' \
	<(cli snapshots list) >/dev/null || fail "snapshot listing does not contain the created snapshot"

cli snapshots rename --name "renamed daemon" "$snapshot_uuid" >/tmp/snapshot-renamed.json
jq -e --arg uuid "$snapshot_uuid" \
	'.uuid == $uuid and .name == "renamed daemon"' \
	/tmp/snapshot-renamed.json >/dev/null || fail "snapshot rename response is invalid"
jq -e --arg uuid "$snapshot_uuid" \
	'.snapshots | length == 1 and .[0].uuid == $uuid and .[0].name == "renamed daemon"' \
	<(cli snapshots list) >/dev/null || fail "snapshot listing does not contain the renamed snapshot"

cli snapshots export --output "$exported_archive" "$snapshot_uuid" >/tmp/snapshot-exported.json
jq -e --arg uuid "$snapshot_uuid" '.uuid == $uuid and .name == "renamed daemon"' \
	/tmp/snapshot-exported.json >/dev/null || fail "snapshot export response is invalid"
cmp "$exported_archive" "$ALMAGEST_E2E_FILE_STORAGE/snapshots/$snapshot_uuid.zip" \
	|| fail "exported snapshot archive differs from the stored archive"

cli snapshots delete "$snapshot_uuid" >/tmp/snapshot-deleted-before-import.json
jq -e '.snapshots | length == 0' <(cli snapshots list) >/dev/null \
	|| fail "snapshot remained after the pre-import delete"
cli snapshots import --name "imported daemon" "$exported_archive" >/tmp/snapshot-imported.json
jq -e --arg uuid "$snapshot_uuid" '.uuid == $uuid and .name == "imported daemon"' \
	/tmp/snapshot-imported.json >/dev/null || fail "snapshot import response is invalid"
cli snapshots rename --name "renamed daemon" "$snapshot_uuid" >/tmp/snapshot-renamed-after-import.json

python3 - "$ALMAGEST_E2E_FILE_STORAGE/snapshots/$snapshot_uuid.zip" <<'PY'
import sys
import zipfile

with zipfile.ZipFile(sys.argv[1]) as archive:
    names = archive.namelist()
    if any(name.startswith("files/snapshots/") for name in names):
        raise SystemExit("snapshot recursively contains snapshots storage")
    config = archive.read("config.hujson").decode("utf-8")
    all_bytes = b"".join(archive.read(name) for name in names if not name.endswith("/"))
    if "Protected" in config or b"protected-socks-password" in all_bytes:
        raise SystemExit("snapshot disclosed protected config")
PY

cat > /tmp/snapshot-mutated.hujson <<'EOF'
{
	"nodes": {
		"Mutated Graph": {"Class": "almagest/MockNetwork"},
	},
}
EOF
cli config apply /tmp/snapshot-mutated.hujson >/tmp/snapshot-config-mutated.json
wait_graph_node "Mutated Graph" || fail "mutated graph did not apply"
cli secrets set --key snapshot-mutable --value after-secret >/tmp/snapshot-secret-mutated.json
cli kv set --key snapshot-default --data after-default >/tmp/snapshot-kv-mutated.json
cli kv set --plugin snapshot-plugin --path nested --key snapshot-plugin-key --data after-plugin >/tmp/snapshot-plugin-mutated.json
cli dns drop --cache snapshot-cache >/tmp/snapshot-dns-mutated.json
cli tls set-builtin-ca --issuer "Mutated snapshot CA" --key-algorithm ed25519 >/tmp/snapshot-ca-mutated.json
mutated_fingerprint="$(cli tls builtin-ca | jq -er '.cert.fingerprint')"
[[ "$mutated_fingerprint" != "$builtin_fingerprint" ]] || fail "built-in CA did not mutate before restore"
printf '%s\n' after-file >"$ALMAGEST_E2E_FILE_STORAGE/future-storage/nested/value.txt"
printf '%s\n' remove-me >"$ALMAGEST_E2E_FILE_STORAGE/future-storage/remove-after-restore.txt"
rm "$ALMAGEST_E2E_FILE_STORAGE/future-storage/value-link"
ln -s nested "$ALMAGEST_E2E_FILE_STORAGE/future-storage/value-link"
rm "$ALMAGEST_E2E_FILE_STORAGE/future-storage/value-link-chain"
ln -s nested "$ALMAGEST_E2E_FILE_STORAGE/future-storage/value-link-chain"

cli snapshots restore "$snapshot_uuid" >/tmp/snapshot-restored.json
jq -e --arg uuid "$snapshot_uuid" '.snapshot.uuid == $uuid and .snapshot.name == "renamed daemon" and .config_written == true' \
	/tmp/snapshot-restored.json >/dev/null || fail "snapshot restore response is invalid"

for name in "SOCKS Snapshot" "SOCKS Listen Network" "SOCKS Proxy Network" "Persistent Mock"; do
	wait_graph_node "$name" || fail "restored node $name is missing"
done
if node_id_by_name "Mutated Graph" >/dev/null 2>&1; then
	fail "mutated graph survived snapshot restore"
fi
restored_socks_id="$(node_id_by_name "SOCKS Snapshot")"
[[ "$restored_socks_id" != "$initial_socks_id" ]] || fail "snapshot restore did not fully rebuild graph nodes"
retry "$ALMAGEST_E2E_TIMEOUT" "restored SOCKS listener" nc -z 127.0.0.1 11108 \
	|| fail "restored SOCKS listener did not start"
retry "$ALMAGEST_E2E_TIMEOUT" "SOCKS request with restored password" \
	curl_expected_socks_password protected-socks-password \
	|| fail "SOCKS rejected the protected password after restore"
assert_socks_password wrong-password failure

[[ "$(cli secrets list | jq -er '.secrets[] | select(.key == "snapshot-mutable") | .value')" == "before-secret" ]] \
	|| fail "mutable secret was not restored"
[[ "$(cli kv get --key snapshot-default | jq -er '.value.data')" == "before-default" ]] \
	|| fail "default KV value was not restored"
[[ "$(cli kv get --plugin snapshot-plugin --path nested --key snapshot-plugin-key | jq -er '.value.data')" == "before-plugin" ]] \
	|| fail "plugin KV value was not restored"
cli dns keys --cache snapshot-cache | jq -e --arg key "$dns_key" '.exists and any(.keys[]; .key == $key)' >/dev/null \
	|| fail "DNS cache was not restored"
[[ "$(cli tls builtin-ca | jq -er '.cert.fingerprint')" == "$builtin_fingerprint" ]] \
	|| fail "built-in TLS CA files were not restored"
[[ "$(<"$ALMAGEST_E2E_FILE_STORAGE/future-storage/nested/value.txt")" == "before-file" ]] \
	|| fail "future file-storage content was not restored"
[[ -L "$ALMAGEST_E2E_FILE_STORAGE/future-storage/value-link" \
	&& "$(readlink "$ALMAGEST_E2E_FILE_STORAGE/future-storage/value-link")" == "nested/value.txt" \
	&& "$(<"$ALMAGEST_E2E_FILE_STORAGE/future-storage/value-link")" == "before-file" ]] \
	|| fail "file-storage symlink was not restored"
[[ -L "$ALMAGEST_E2E_FILE_STORAGE/future-storage/value-link-chain" \
	&& "$(readlink "$ALMAGEST_E2E_FILE_STORAGE/future-storage/value-link-chain")" == "value-link" \
	&& "$(<"$ALMAGEST_E2E_FILE_STORAGE/future-storage/value-link-chain")" == "before-file" ]] \
	|| fail "file-storage symlink chain was not restored"
[[ ! -e "$ALMAGEST_E2E_FILE_STORAGE/future-storage/remove-after-restore.txt" ]] \
	|| fail "file absent from snapshot survived restore"
[[ "$(sha256sum "$E2E_CONFIG_DIR/protected_alm.hujson" | cut -d ' ' -f 1)" == "$protected_checksum" ]] \
	|| fail "protected config changed during restore"
grep -F 'SOCKS Snapshot' "$E2E_CONFIG_DIR/alm.hujson" >/dev/null \
	|| fail "regular config file was not overwritten from snapshot"
if grep -F 'Mutated Graph' "$E2E_CONFIG_DIR/alm.hujson" >/dev/null; then
	fail "mutated config remained on disk after restore"
fi
[[ -f "$ALMAGEST_E2E_FILE_STORAGE/snapshots/$snapshot_uuid.zip" ]] \
	|| fail "restoring a snapshot removed the snapshot archive"

# Mutate persisted state, then prove that --snapshot restores the exported
# archive before bbolt and the engine open.
cli kv set --key snapshot-default --data before-startup-load >/tmp/snapshot-kv-before-startup-load.json
cat > /tmp/snapshot-startup-mutated.hujson <<'EOF'
{
	"nodes": {
		"Startup Mutated Graph": {"Class": "almagest/MockNetwork"},
	},
}
EOF
cli config apply /tmp/snapshot-startup-mutated.hujson >/tmp/snapshot-startup-config-mutated.json
wait_graph_node "Startup Mutated Graph" || fail "startup mutation did not apply"
stop_daemon
export ALMAGEST_E2E_STARTUP_SNAPSHOT="$exported_archive"
start_daemon
trap cleanup EXIT
wait_graph_node "SOCKS Snapshot" || fail "startup snapshot graph was not loaded"
if node_id_by_name "Startup Mutated Graph" >/dev/null 2>&1; then
	fail "mutated graph survived startup snapshot loading"
fi
[[ "$(cli kv get --key snapshot-default | jq -er '.value.data')" == "before-default" ]] \
	|| fail "startup snapshot did not restore bbolt data"
[[ "$(<"$ALMAGEST_E2E_FILE_STORAGE/future-storage/nested/value.txt")" == "before-file" ]] \
	|| fail "startup snapshot did not restore file storage"

cli snapshots delete "$snapshot_uuid" >/tmp/snapshot-deleted.json
jq -e '. == {}' /tmp/snapshot-deleted.json >/dev/null || fail "snapshot delete response is invalid"
jq -e '.snapshots | length == 0' <(cli snapshots list) >/dev/null \
	|| fail "deleted snapshot remains in the snapshot listing"
[[ ! -e "$ALMAGEST_E2E_FILE_STORAGE/snapshots/$snapshot_uuid.zip" ]] \
	|| fail "deleted snapshot archive remains on disk"

log "case passed"
