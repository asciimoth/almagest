#!/usr/bin/env python3
"""Validate isolated Windows packet-flow captures and their observation log."""

import argparse
import json
from pathlib import Path


PATHS = {"tunnel", "underlay", "none", "tunnel-or-none-stopped",
         "underlay-or-none-stopped"}
PROFILES = {
    "ipv4": {"tcp4", "udp4"},
    "ipv6": {"tcp6", "udp6"},
    "dual": {"tcp4", "udp4", "tcp6", "udp6"},
}
ROUTING_CASES = {
    "included": "tunnel",
    "excluded": "underlay",
    "descendant": "underlay",
    "ipc-target": "tunnel",
}
SPECIAL_CASES = {
    "nested-wireguard": {("dual", "udp4"), ("dual", "udp6")},
    "application-dns": {("ipv4", "udp4"), ("ipv6", "udp6"),
                        ("dual", "udp4"), ("dual", "udp6")},
    "shared-resolver-dns": {("ipv4", "ip4"), ("ipv6", "ip6"),
                            ("dual", "ip4"), ("dual", "ip6")},
    "underlay-failure": {("dual", "tcp4"), ("dual", "udp4"),
                         ("dual", "tcp6"), ("dual", "udp6")},
    "underlay-reconnect": {("dual", "tcp4"), ("dual", "udp4"),
                           ("dual", "tcp6"), ("dual", "udp6")},
}
BACKEND_PUBLIC_CASES = {
    "E01-included-process-tunnel": "tunnel",
    "E02-excluded-process-underlay": "underlay",
    "E03-excluded-descendant-underlay": "underlay",
    "E04-pre-existing-ipc-target-tunnel": "tunnel",
    "E05-explicit-application-dns-underlay": "underlay",
    "E06-shared-windows-dns-client": "underlay",
}
BACKEND_REQUIRED_CASES = {
    "N21-new-tcp4-fails", "N21-new-udp4-fails",
    "N21-new-tcp6-fails", "N21-new-udp6-fails",
    "N24-underlay-recovery",
}


def required_coverage():
    required = {}
    for case in ROUTING_CASES:
        required[case] = {
            (profile, network)
            for profile, networks in PROFILES.items()
            for network in networks
        }
    required.update(SPECIAL_CASES)
    return required


def fail(message):
    raise ValueError(message)


def parse_observation(value, line, tokens):
    if not isinstance(value, dict):
        fail(f"line {line}: observation is not an object")
    token = value.get("token")
    if not isinstance(token, str) or not token:
        fail(f"line {line}: token is not a non-empty string")
    try:
        encoded = token.encode("ascii")
    except UnicodeEncodeError as error:
        raise ValueError(f"line {line}: token is not ASCII") from error
    if token in tokens:
        fail(f"line {line}: duplicate token {token}")
    if any(token in old or old in token for old in tokens):
        fail(f"line {line}: token overlaps another token")
    tokens.add(token)
    if value.get("expectedPath") not in PATHS:
        fail(f"line {line}: unknown expected path {value.get('expectedPath')!r}")
    return encoded


def validate_metadata(value, line, coverage, backend_coverage, backend_cases,
                      previous):
    case = value.get("case")
    if value.get("positiveControl") is True:
        if not isinstance(case, str) or not case.lower().startswith(("control-", "ctrl-")):
            fail(f"line {line}: positive control has no control case")
        return
    if case in BACKEND_PUBLIC_CASES:
        profile, network = value.get("profile"), value.get("network")
        if profile not in PROFILES:
            fail(f"line {line}: unknown backend profile {profile!r}")
        expected_networks = ({"ip4", "ip6"} if case == "E06-shared-windows-dns-client"
                             else PROFILES[profile])
        if network not in expected_networks:
            fail(f"line {line}: backend network {network!r} is invalid for {case}/{profile}")
        if previous is None or previous.get("positiveControl") is not True:
            fail(f"line {line}: backend assertion lacks its immediately preceding positive control")
        if value.get("controlToken") != previous.get("token"):
            fail(f"line {line}: backend assertion does not name its positive control")
        expected = BACKEND_PUBLIC_CASES[case]
        if value.get("expectedPath") != expected:
            fail(f"line {line}: {case} has incorrect expected path")
        if previous.get("expectedPath") == expected:
            fail(f"line {line}: backend positive control does not prove the other link")
        if case == "E04-pre-existing-ipc-target-tunnel" and not (
            value.get("preExistingTarget") is True
            and value.get("requestingRole") == "excluded"
        ):
            fail(f"line {line}: backend IPC evidence does not identify target ownership")
        if case == "E06-shared-windows-dns-client" and not value.get("limitation"):
            fail(f"line {line}: shared resolver evidence lacks its attribution limitation")
        backend_coverage[case].add((profile, network))
        return
    if isinstance(case, str) and case.startswith("N"):
        backend_cases.add(case)
        return
    if case not in coverage:
        fail(f"line {line}: unknown packet-flow case {case!r}")
    profile, network = value.get("profile"), value.get("network")
    if profile not in PROFILES:
        fail(f"line {line}: unknown profile {profile!r}")
    if (profile, network) not in coverage[case]:
        fail(f"line {line}: network {network!r} is invalid for {case}/{profile}")
    if previous is None or previous.get("positiveControl") is not True:
        fail(f"line {line}: assertion lacks its immediately preceding positive control")
    if value.get("controlToken") != previous.get("token"):
        fail(f"line {line}: assertion does not name its positive control")
    expected = value["expectedPath"]
    control_path = previous.get("expectedPath")
    if expected == "none":
        if control_path not in {"tunnel", "underlay"}:
            fail(f"line {line}: absence control does not prove capture visibility")
    elif control_path == expected:
        fail(f"line {line}: positive control does not prove the other link")
    if case in ROUTING_CASES and expected != ROUTING_CASES[case]:
        fail(f"line {line}: {case} has incorrect expected path {expected}")
    if case == "underlay-failure" and expected != "none":
        fail(f"line {line}: underlay failure must fail closed")
    if case == "underlay-reconnect" and expected != "tunnel":
        fail(f"line {line}: reconnect must restore the tunnel path")
    if case == "ipc-target" and not (
        value.get("preExistingTarget") is True
        and value.get("requestingRole") == "excluded"
    ):
        fail(f"line {line}: IPC evidence does not identify target ownership")
    if case == "shared-resolver-dns" and value.get("attribution") != "shared-resolver":
        fail(f"line {line}: shared resolver traffic is attributed to an application")
    coverage[case].remove((profile, network))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--observations", required=True, type=Path)
    parser.add_argument("--tunnel", required=True, type=Path)
    parser.add_argument("--underlay", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    args.output.unlink(missing_ok=True)
    tunnel, underlay = args.tunnel.read_bytes(), args.underlay.read_bytes()
    coverage = required_coverage()
    backend_coverage = {case: set() for case in BACKEND_PUBLIC_CASES}
    backend_cases = set()
    results, errors, tokens, previous = [], [], set(), None
    with args.observations.open(encoding="utf-8") as source:
        for line, text in enumerate(source, 1):
            if not text.strip():
                continue
            value = json.loads(text)
            marker = parse_observation(value, line, tokens)
            validate_metadata(value, line, coverage, backend_coverage,
                              backend_cases, previous)
            counts = (tunnel.count(marker), underlay.count(marker))
            valid = {
                "tunnel": counts[0] > 0 and counts[1] == 0,
                "underlay": counts[1] > 0 and counts[0] == 0,
                "none": counts == (0, 0),
                "tunnel-or-none-stopped": counts[1] == 0,
                "underlay-or-none-stopped": counts[0] == 0,
            }[value["expectedPath"]]
            result = dict(value, tunnelPacketMarkers=counts[0],
                          underlayPacketMarkers=counts[1], packetPathValid=valid)
            results.append(result)
            if not valid:
                errors.append(f"line {line}: {value['token']} expected {value['expectedPath']}, "
                              f"found tunnel={counts[0]} underlay={counts[1]}")
            previous = value
    args.output.write_text(json.dumps(results, indent=2) + "\n", encoding="utf-8")
    if not results:
        errors.append("packet-flow observation file is empty")
    missing = [f"{case}: {', '.join('/'.join(item) for item in sorted(items))}"
               for case, items in coverage.items() if items]
    generic_seen = any(item.get("case") in required_coverage() for item in results)
    if generic_seen and missing:
        errors.append("missing packet-flow coverage: " + "; ".join(missing))
    backend_seen = any(item.get("case") in BACKEND_PUBLIC_CASES for item in results)
    if backend_seen:
        missing_backend = []
        for case, expected in BACKEND_PUBLIC_CASES.items():
            if case == "E05-explicit-application-dns-underlay":
                required = {(profile, network) for profile, networks in PROFILES.items()
                            for network in networks if network.startswith("udp")}
            elif case == "E06-shared-windows-dns-client":
                required = {(profile, "ip4") for profile in ("ipv4", "dual")} | {
                    (profile, "ip6") for profile in ("ipv6", "dual")}
            else:
                required = {(profile, network) for profile, networks in PROFILES.items()
                            for network in networks}
            absent = required - backend_coverage[case]
            if absent:
                missing_backend.append(
                    f"{case}: {', '.join('/'.join(item) for item in sorted(absent))}")
        missing_cases = BACKEND_REQUIRED_CASES - backend_cases
        if missing_backend:
            errors.append("missing backend public coverage: " + "; ".join(missing_backend))
        if missing_cases:
            errors.append("missing backend lifecycle coverage: " +
                          ", ".join(sorted(missing_cases)))
    if errors:
        raise SystemExit("\n".join(errors))
    print(f"Validated {len(results)} packet observations")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, json.JSONDecodeError) as error:
        raise SystemExit(f"flow-pcap.py: {error}") from error
