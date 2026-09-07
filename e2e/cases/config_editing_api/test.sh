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

log "case passed"
