#!/usr/bin/env bash
set -euo pipefail

root="$(mktemp -d)"
trap 'rm -rf "$root"' EXIT

# shellcheck disable=SC1091 # The path is resolved from this script's directory.
source "$(dirname "${BASH_SOURCE[0]}")/amnesia_peer_handoff.sh"

peer="$root/peer"
ack="$root/ack"
key_one='AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA='
key_two='AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE='

amnesia_peer_publish "$peer" 10.205.10.1 "$key_one"
record="$(amnesia_peer_record "$peer")"
[[ "$record" == "10.205.10.1 $key_one $(printf '%064d' 0)" ]] || {
	echo "valid peer record was not parsed: $record" >&2
	exit 1
}

# These are the states that a truncate-and-write publisher can expose. The
# monitor must treat each one as a retry condition instead of exiting.
for invalid in '' '10.205.10.1' '10.205.10.1 incomplete' \
	"10.205.10.1 $key_one trailing"; do
	printf '%s' "$invalid" >"$peer"
	if amnesia_peer_record "$peer" >/dev/null; then
		echo "invalid peer record was accepted: $invalid" >&2
		exit 1
	fi
done

# Stress the publication boundary. Each reader must observe one complete
# generation, even while the writer replaces the file repeatedly.
amnesia_peer_publish "$peer" 10.205.10.1 "$key_one"
(
	for ((i = 0; i < 4000; i++)); do
		if ((i % 2)); then
			amnesia_peer_publish "$peer" 10.205.10.1 "$key_one"
		else
			amnesia_peer_publish "$peer" 10.205.10.2 "$key_two"
		fi
	done
) &
writer_pid="$!"
for ((i = 0; i < 4000; i++)); do
	record="$(amnesia_peer_record "$peer")" || {
		echo "atomic peer publication exposed an invalid record" >&2
		exit 1
	}
	case "$record" in
		"10.205.10.1 $key_one "* | "10.205.10.2 $key_two "*) ;;
		*)
			echo "atomic peer publication mixed generations: $record" >&2
			exit 1
			;;
	esac
done
wait "$writer_pid"

amnesia_peer_acknowledge "$ack" "$key_one"
amnesia_peer_acknowledged "$ack" "$key_one" || {
	echo "current peer acknowledgement was not accepted" >&2
	exit 1
}
if amnesia_peer_acknowledged "$ack" "$key_two"; then
	echo "stale peer acknowledgement was accepted" >&2
	exit 1
fi
amnesia_peer_acknowledge "$ack" "$key_two"
amnesia_peer_acknowledged "$ack" "$key_two" || {
	echo "replacement peer acknowledgement was not accepted" >&2
	exit 1
}

echo "Amnesia peer handoff tests passed"
