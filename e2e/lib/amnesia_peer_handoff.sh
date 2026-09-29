#!/usr/bin/env bash

# Publish a complete peer record with one atomic rename. Readers must not see
# the empty or partial file that a truncate-and-write update can expose.
amnesia_peer_publish() {
	local path="$1"
	local client_ip="$2"
	local public_key="$3"
	local temporary="${path}.tmp.${BASHPID}.${RANDOM}"

	if ! printf '%s %s\n' "$client_ip" "$public_key" >"$temporary"; then
		rm -f -- "$temporary"
		return 1
	fi
	if ! mv -f -- "$temporary" "$path"; then
		rm -f -- "$temporary"
		return 1
	fi
}

# Print a validated record as: client IP, public key, hexadecimal public key.
# A concurrent or malformed record is an expected retry condition.
amnesia_peer_record() {
	local path="$1"
	local client_ip public_key extra public_key_hex

	[[ -s "$path" ]] || return 1
	IFS=' ' read -r client_ip public_key extra <"$path" || return 1
	[[ -n "$client_ip" && -n "$public_key" && -z "$extra" ]] || return 1
	[[ "$client_ip" != *[[:space:]]* ]] || return 1
	[[ "$public_key" =~ ^[A-Za-z0-9+/]{43}=$ ]] || return 1
	public_key_hex="$(printf '%s' "$public_key" | base64 -d 2>/dev/null \
		| od -An -tx1 -v | tr -d ' \n')" || return 1
	[[ ${#public_key_hex} -eq 64 ]] || return 1

	printf '%s %s %s\n' "$client_ip" "$public_key" "$public_key_hex"
}

amnesia_peer_acknowledge() {
	local path="$1"
	local public_key="$2"
	local temporary="${path}.tmp.${BASHPID}.${RANDOM}"

	if ! printf '%s\n' "$public_key" >"$temporary"; then
		rm -f -- "$temporary"
		return 1
	fi
	if ! mv -f -- "$temporary" "$path"; then
		rm -f -- "$temporary"
		return 1
	fi
}

amnesia_peer_acknowledged() {
	local path="$1"
	local public_key="$2"

	[[ -r "$path" && "$(<"$path")" == "$public_key" ]]
}
