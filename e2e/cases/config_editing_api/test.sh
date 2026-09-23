#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

assert_socks_body() {
	local port="$1"
	local host="$2"
	local target_port="$3"
	local expected="$4"
	local output="/tmp/config-edit-${port}-${host}.txt"

	retry "$ALMAGEST_E2E_TIMEOUT" "SOCKS $port returns $expected for $host" \
		curl -g --fail --silent --show-error \
			--socks5-hostname "127.0.0.1:$port" \
			"http://${host}:${target_port}/" \
			--output "$output" \
		|| fail "SOCKS $port request to $host:$target_port failed"

	[[ "$(<"$output")" == "$expected" ]] \
		|| fail "SOCKS $port returned unexpected body: $(<"$output")"
}

assert_original_ids() {
	local before="$1"
	local after="$2"
	jq -e --argfile before "$before" --argfile after "$after" '
		$before.nodes
		| to_entries
		| all(. as $entry | $after.nodes[$entry.key].ID == $entry.value.ID)
	' "$before" >/dev/null || fail "an original node ID changed during config editing"
}

assert_config_snapshots_stable() {
	local count="$1"
	local stage="$2"
	local base="/tmp/config-edit-stable-${stage}-base.json"
	local current="/tmp/config-edit-stable-${stage}-current.json"
	local base_text="/tmp/config-edit-stable-${stage}-base.hujson"
	local current_text="/tmp/config-edit-stable-${stage}-current.hujson"
	local expected_revision
	local current_revision

	cli config snapshot >"$base"
	expected_revision="$(jq -er '.revision' "$base")"
	jq -er '.text' "$base" >"$base_text"
	for ((iteration = 1; iteration <= count; iteration++)); do
		cli config snapshot >"$current"
		current_revision="$(jq -er '.revision' "$current")"
		[[ "$current_revision" == "$expected_revision" ]] \
			|| fail "$stage config snapshot revision changed on iteration $iteration"
		jq -er '.text' "$current" >"$current_text"
		cmp -s "$base_text" "$current_text" \
			|| fail "$stage config snapshot text changed on iteration $iteration"
	done
}

assert_config_rejected() {
	local path="$1"
	local expected_error="$2"
	local description="$3"
	local revision="$4"
	local config_checksum
	local current_revision
	config_checksum="$(sha256sum "$E2E_CONFIG_DIR/alm.hujson" | cut -d ' ' -f 1)"
	cli graph map >"/tmp/config-edit-rejected-${description}-before-map.json"

	if cli config apply --revision "$revision" "$path" \
		>"/tmp/config-edit-rejected-${description}.json" \
		2>"/tmp/config-edit-rejected-${description}.err"; then
		fail "$description config was accepted"
	fi
	grep -F "$expected_error" "/tmp/config-edit-rejected-${description}.err" >/dev/null \
		|| fail "$description config rejection did not report: $expected_error"
	[[ "$(sha256sum "$E2E_CONFIG_DIR/alm.hujson" | cut -d ' ' -f 1)" == "$config_checksum" ]] \
		|| fail "$description config rejection changed the saved config"
	cli graph map >"/tmp/config-edit-rejected-${description}-after-map.json"
	assert_original_ids \
		"/tmp/config-edit-rejected-${description}-before-map.json" \
		"/tmp/config-edit-rejected-${description}-after-map.json"
	current_revision="$(cli config snapshot | jq -er '.revision')"
	[[ "$current_revision" == "$revision" ]] \
		|| fail "$description config rejection changed the snapshot revision"
}

start_daemon

for name in "Router Proxy" Router "Mock A" "Mock B" "Server A" "Server B" "Stable Proxy" "Stable Mock" "Stable Server"; do
	wait_graph_node "$name" || fail "initial node $name is missing"
done

assert_socks_body 1081 alpha 8080 router-a
assert_socks_body 1081 anything 8080 router-b
assert_socks_body 1082 127.0.0.1 8081 stable

cli graph map >/tmp/config-edit-before-map.json
stable_proxy_link="$(link_id_by_port_names "Stable Proxy" proxy "Stable Mock" Network)"
stable_server_link="$(link_id_by_port_names "Stable Server" Network "Stable Mock" Network)"

cli config snapshot >/tmp/config-edit-snapshot.json
revision_before="$(jq -er '.revision' /tmp/config-edit-snapshot.json)"
jq -er '.read_only == false and (.text | contains("config-api-protected") | not) and (.text | contains("config-api-protected-value") | not)' \
	/tmp/config-edit-snapshot.json >/dev/null \
	|| fail "config snapshot is read-only or disclosed protected config"
jq -er '.text' /tmp/config-edit-snapshot.json >/tmp/config-edit-stage0.hujson
assert_config_snapshots_stable 16 initial

python3 - /tmp/config-edit-stage0.hujson /tmp/config-edit-stage1.hujson <<'PY'
import pathlib
import re
import sys

source = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
pattern = r'("rules"\s*:\s*)"ADDR_S alpha\\nSLOT 1\\n\\nTRUE\\nSLOT 2"'
source, count = re.subn(pattern, r'\1"ADDR_S alpha\\nSLOT 2\\n\\nTRUE\\nSLOT 1"', source)
if count != 1:
    raise SystemExit("router rules marker was not found exactly once")
pathlib.Path(sys.argv[2]).write_text(source, encoding="utf-8")
PY

cli config apply --revision "$revision_before" /tmp/config-edit-stage1.hujson >/tmp/config-edit-stage1-result.json
jq -e '
	.changes.reinitialized == ["Router"]
	and (.changes.added // []) == []
	and (.changes.removed // []) == []
' /tmp/config-edit-stage1-result.json >/dev/null \
	|| fail "router config edit rebuilt unexpected nodes"

if cli config apply --revision "$revision_before" /tmp/config-edit-stage1.hujson >/tmp/config-edit-stale.json 2>/tmp/config-edit-stale.err; then
	fail "stale config revision was accepted"
fi
grep -F "conflict: config revision conflict" /tmp/config-edit-stale.err >/dev/null \
	|| fail "stale config update did not report a revision conflict"

assert_socks_body 1081 alpha 8080 router-b
assert_socks_body 1081 anything 8080 router-a
assert_socks_body 1082 127.0.0.1 8081 stable

cli config snapshot >/tmp/config-edit-stage1-snapshot.json
revision_stage1="$(jq -er '.revision' /tmp/config-edit-stage1-snapshot.json)"
jq -er '.text' /tmp/config-edit-stage1-snapshot.json >/tmp/config-edit-stage1-canonical.hujson

# Reject malformed and duplicate-link documents before they can mutate the
# graph. Both failures must keep the saved config, node IDs, and live traffic.
printf '%s\n' '{"nodes": {' >/tmp/config-edit-malformed.hujson
assert_config_rejected \
	/tmp/config-edit-malformed.hujson \
	"invalid editable config" \
	malformed \
	"$revision_stage1"

python3 - /tmp/config-edit-stage1-canonical.hujson /tmp/config-edit-duplicate-link.hujson <<'PY'
import pathlib
import sys

source = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
marker = '"slot 1 -> [Mock A] Network",'
if source.count(marker) != 1:
    raise SystemExit("router link marker was not found exactly once")
pathlib.Path(sys.argv[2]).write_text(
    source.replace(marker, marker + "\n\t\t\t\t" + marker, 1),
    encoding="utf-8",
)
PY
assert_config_rejected \
	/tmp/config-edit-duplicate-link.hujson \
	"duplicate link" \
	duplicate-link \
	"$revision_stage1"
assert_socks_body 1081 alpha 8080 router-b
assert_socks_body 1082 127.0.0.1 8081 stable

# Force reconciliation to rebuild a live HTTP server before it discovers an
# unavailable router port. The rejected update must roll the server and links
# back to their previous working state without changing the saved config.
python3 - /tmp/config-edit-stage1-canonical.hujson /tmp/config-edit-rollback.hujson <<'PY'
import pathlib
import sys

source = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
replacements = {
    '"slot 1 -> [Mock A] Network"': '"missing -> [Mock A] Network"',
    '"response": "router-a"': '"response": "must-be-rolled-back"',
}
for old, new in replacements.items():
    if source.count(old) != 1:
        raise SystemExit(f"config marker was not found exactly once: {old}")
    source = source.replace(old, new, 1)
pathlib.Path(sys.argv[2]).write_text(source, encoding="utf-8")
PY
assert_config_rejected \
	/tmp/config-edit-rollback.hujson \
	"endpoint port is not available" \
	rollback \
	"$revision_stage1"
assert_socks_body 1081 alpha 8080 router-b
assert_socks_body 1081 anything 8080 router-a
assert_socks_body 1082 127.0.0.1 8081 stable

# A position-only edit must update graph metadata without rebuilding nodes or
# links. Removing the position must have the same property.
python3 - /tmp/config-edit-stage1-canonical.hujson /tmp/config-edit-position.hujson <<'PY'
import pathlib
import sys

source = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
marker = '"Stable Mock": {"Class": "almagest/MockNetwork", "allowAnyHost": true}'
replacement = '"Stable Mock": {"Class": "almagest/MockNetwork", "Pos": "120 240", "allowAnyHost": true}'
if source.count(marker) != 1:
    raise SystemExit("stable mock marker was not found exactly once")
pathlib.Path(sys.argv[2]).write_text(source.replace(marker, replacement, 1), encoding="utf-8")
PY
cli config apply --revision "$revision_stage1" /tmp/config-edit-position.hujson \
	>/tmp/config-edit-position-result.json
jq -e '
	.changes.positions_updated == ["Stable Mock"]
	and ((.changes | del(.positions_updated)) == {})
' /tmp/config-edit-position-result.json >/dev/null \
	|| fail "position-only config edit changed runtime graph parts"
[[ "$(cli graph node --name "Stable Mock" | jq -er '.snapshot.position')" == "120 240" ]] \
	|| fail "position-only config edit did not update Stable Mock"
revision_position="$(jq -er '.snapshot.revision' /tmp/config-edit-position-result.json)"
cli config apply --revision "$revision_position" /tmp/config-edit-stage1-canonical.hujson \
	>/tmp/config-edit-position-restore-result.json
jq -e '
	.changes.positions_updated == ["Stable Mock"]
	and ((.changes | del(.positions_updated)) == {})
' /tmp/config-edit-position-restore-result.json >/dev/null \
	|| fail "removing a config position changed runtime graph parts"
revision_stage1="$(jq -er '.snapshot.revision' /tmp/config-edit-position-restore-result.json)"
assert_socks_body 1082 127.0.0.1 8081 stable

# Rewire one live proxy to another mock network without rebuilding either
# endpoint, verify traffic through the new link, and then restore the link.
python3 - /tmp/config-edit-stage1-canonical.hujson /tmp/config-edit-rewire.hujson <<'PY'
import pathlib
import re
import sys

source = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
pattern = r'("Links"\s*:\s*\[\s*)"proxy -> \[Router\] Network"(\s*\])'
source, count = re.subn(
    pattern,
    r'\1"proxy -> [Mock A] Network"\2',
    source,
)
if count != 1:
    raise SystemExit("router proxy link marker was not found exactly once")
pathlib.Path(sys.argv[2]).write_text(source, encoding="utf-8")
PY
cli config apply --revision "$revision_stage1" /tmp/config-edit-rewire.hujson \
	>/tmp/config-edit-rewire-result.json
jq -e '
	.changes.links_added == ["proxy -> [Mock A] Network"]
	and .changes.links_removed == ["proxy -> [Router] Network"]
	and ((.changes | del(.links_added, .links_removed)) == {})
' /tmp/config-edit-rewire-result.json >/dev/null \
	|| fail "link-only config edit changed runtime nodes"
assert_socks_body 1081 anything 8080 router-a
revision_rewire="$(jq -er '.snapshot.revision' /tmp/config-edit-rewire-result.json)"
cli config apply --revision "$revision_rewire" /tmp/config-edit-stage1-canonical.hujson \
	>/tmp/config-edit-rewire-restore-result.json
jq -e '
	.changes.links_added == ["proxy -> [Router] Network"]
	and .changes.links_removed == ["proxy -> [Mock A] Network"]
	and ((.changes | del(.links_added, .links_removed)) == {})
' /tmp/config-edit-rewire-restore-result.json >/dev/null \
	|| fail "restoring a config link changed runtime nodes"
revision_stage1="$(jq -er '.snapshot.revision' /tmp/config-edit-rewire-restore-result.json)"
assert_socks_body 1081 alpha 8080 router-b
assert_socks_body 1081 anything 8080 router-a
assert_socks_body 1082 127.0.0.1 8081 stable

python3 - /tmp/config-edit-stage1-canonical.hujson /tmp/config-edit-stage2.hujson <<'PY'
import pathlib
import sys

source = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
marker = '"nodes": {'
addition = r'''
		"Added Proxy": {
			"Class": "almagest/LocalProxy",
			"Links": ["proxy -> [Added Mock] Network"],
			"socksHost": "127.0.0.1",
			"socksPort": 1083,
		},
		"Added Mock": {"Class": "almagest/MockNetwork"},
		"Added Server": {
			"Class": "almagest/HttpServer",
			"Links": ["Network -> [Added Mock] Network"],
			"host": "127.0.0.1",
			"port": 9090,
			"response": "added",
		},'''
if source.count(marker) != 1:
    raise SystemExit("nodes marker was not found exactly once")
pathlib.Path(sys.argv[2]).write_text(source.replace(marker, marker + addition, 1), encoding="utf-8")
PY

cli config apply --revision "$revision_stage1" /tmp/config-edit-stage2.hujson >/tmp/config-edit-stage2-result.json
jq -e '
	.changes.added == ["Added Mock", "Added Proxy", "Added Server"]
	and ((.changes.links_added // []) | length) == 2
	and (.changes.reinitialized // []) == []
' /tmp/config-edit-stage2-result.json >/dev/null \
	|| fail "adding the independent config branch changed unexpected graph parts"

for name in "Added Proxy" "Added Mock" "Added Server"; do
	wait_graph_node "$name" || fail "added node $name is missing"
done
assert_socks_body 1083 127.0.0.1 9090 added
assert_socks_body 1082 127.0.0.1 8081 stable

cli config snapshot >/tmp/config-edit-stage2-snapshot.json
revision_stage2="$(jq -er '.revision' /tmp/config-edit-stage2-snapshot.json)"
cli config apply --revision "$revision_stage2" /tmp/config-edit-stage1-canonical.hujson >/tmp/config-edit-delete-result.json
jq -e '
	.changes.removed == ["Added Mock", "Added Proxy", "Added Server"]
	and ((.changes.links_removed // []) | length) == 2
	and (.changes.reinitialized // []) == []
' /tmp/config-edit-delete-result.json >/dev/null \
	|| fail "deleting the independent config branch changed unexpected graph parts"

if node_id_by_name "Added Proxy" >/dev/null 2>&1; then
	fail "deleted Added Proxy is still present"
fi
if curl --fail --silent --show-error --max-time 2 \
	--socks5-hostname 127.0.0.1:1083 \
	http://127.0.0.1:9090/ >/tmp/config-edit-deleted-response.txt 2>/tmp/config-edit-deleted.err; then
	fail "deleted config branch still accepts traffic"
fi

cli graph map >/tmp/config-edit-after-map.json
assert_original_ids /tmp/config-edit-before-map.json /tmp/config-edit-after-map.json
[[ "$(link_id_by_port_names "Stable Proxy" proxy "Stable Mock" Network)" == "$stable_proxy_link" ]] \
	|| fail "stable proxy link ID changed"
[[ "$(link_id_by_port_names "Stable Server" Network "Stable Mock" Network)" == "$stable_server_link" ]] \
	|| fail "stable server link ID changed"
assert_socks_body 1082 127.0.0.1 8081 stable

python3 - /tmp/config-edit-stage1-canonical.hujson /tmp/config-edit-protected.hujson <<'PY'
import pathlib
import sys

source = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
index = source.index("{") + 1
injected = '\n\t"Protected": {"Secrets": {"injected": {"Value": "must-not-appear"}}},'
pathlib.Path(sys.argv[2]).write_text(source[:index] + injected + source[index:], encoding="utf-8")
PY
if cli config apply /tmp/config-edit-protected.hujson >/tmp/config-edit-protected.json 2>/tmp/config-edit-protected.err; then
	fail "config API accepted embedded protected data"
fi
grep -F "editable config must not contain Protected" /tmp/config-edit-protected.err >/dev/null \
	|| fail "protected config rejection was not reported"
if cli config get | grep -E 'must-not-appear|config-api-protected-value|config-api-protected-token' >/dev/null; then
	fail "config API exposed protected data after rejected update"
fi

# Applying the current canonical document is a graph no-op. It must keep all
# node and link IDs and return an empty change set.
cli config snapshot >/tmp/config-edit-before-noop.json
revision_before_noop="$(jq -er '.revision' /tmp/config-edit-before-noop.json)"
jq -er '.text' /tmp/config-edit-before-noop.json >/tmp/config-edit-noop.hujson
cli config apply --revision "$revision_before_noop" /tmp/config-edit-noop.hujson \
	>/tmp/config-edit-noop-result.json
jq -e --arg revision "$revision_before_noop" '
	.changes == {}
	and .snapshot.revision == $revision
' /tmp/config-edit-noop-result.json >/dev/null \
	|| fail "equivalent config apply changed the runtime graph"
jq -er '.snapshot.text' /tmp/config-edit-noop-result.json \
	>/tmp/config-edit-noop-result.hujson
cmp -s /tmp/config-edit-noop.hujson /tmp/config-edit-noop-result.hujson \
	|| fail "equivalent config apply changed the canonical document"
cli graph map >/tmp/config-edit-after-noop-map.json
assert_original_ids /tmp/config-edit-after-map.json /tmp/config-edit-after-noop-map.json
[[ "$(link_id_by_port_names "Stable Proxy" proxy "Stable Mock" Network)" == "$stable_proxy_link" ]] \
	|| fail "no-op config apply changed the stable proxy link ID"
[[ "$(link_id_by_port_names "Stable Server" Network "Stable Mock" Network)" == "$stable_server_link" ]] \
	|| fail "no-op config apply changed the stable server link ID"
assert_config_snapshots_stable 8 post-noop

# Restart from the autosaved regular config and prove that the final graph and
# protected secret boundary remain valid after startup reconstruction.
stop_daemon
start_daemon
for name in "Router Proxy" Router "Mock A" "Mock B" "Server A" "Server B" "Stable Proxy" "Stable Mock" "Stable Server"; do
	wait_graph_node "$name" || fail "restarted node $name is missing"
done
assert_socks_body 1081 alpha 8080 router-b
assert_socks_body 1081 anything 8080 router-a
assert_socks_body 1082 127.0.0.1 8081 stable
if cli config get | grep -E 'must-not-appear|config-api-protected-value|config-api-protected-token' >/dev/null; then
	fail "config API exposed protected data after daemon restart"
fi
assert_config_snapshots_stable 8 restart

log "case passed"
