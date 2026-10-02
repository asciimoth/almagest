#!/usr/bin/env bash
set -euo pipefail

evidence=${1:-${WINDOWS_FLOW_EVIDENCE_DIR:-}}
if [[ -z $evidence ]]; then
    printf 'test-windows-flow: supply an evidence directory or set WINDOWS_FLOW_EVIDENCE_DIR\n' >&2
    exit 2
fi
evidence=$(realpath -e "$evidence")
for name in packet-flow-observations.jsonl tunnel.pcap underlay.pcap; do
    [[ -f $evidence/$name ]] || { printf 'test-windows-flow: missing %s/%s\n' "$evidence" "$name" >&2; exit 1; }
done

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
python3 "$repo_root/dev/winvm/tools/flow-pcap.py" \
    --observations "$evidence/packet-flow-observations.jsonl" \
    --tunnel "$evidence/tunnel.pcap" \
    --underlay "$evidence/underlay.pcap" \
    --output "$evidence/packet-flow-evidence.json"
