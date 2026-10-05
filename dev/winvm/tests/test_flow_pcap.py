"""Negative and positive tests for independent packet-capture validation."""

import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "tools" / "flow-pcap.py"
SPEC = importlib.util.spec_from_file_location("flow_pcap", SCRIPT)
FLOW = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(FLOW)


class FlowPcapTests(unittest.TestCase):
    def observations(self):
        values, tunnel, underlay = [], bytearray(), bytearray()
        sequence = 0
        expected = dict(FLOW.ROUTING_CASES)
        expected.update({
            "nested-wireguard": "tunnel",
            "application-dns": "underlay",
            "shared-resolver-dns": "underlay",
            "underlay-failure": "none",
            "underlay-reconnect": "tunnel",
        })
        for case, items in FLOW.required_coverage().items():
            for profile, network in sorted(items):
                sequence += 1
                control = f"CONTROL_{sequence:04d}"
                token = f"ASSERT_{sequence:04d}"
                path = expected[case]
                control_path = "underlay" if path == "tunnel" else "tunnel"
                values.append({"token": control, "case": f"control-{sequence}",
                               "positiveControl": True, "expectedPath": control_path})
                assertion = {"token": token, "case": case, "profile": profile,
                             "network": network, "expectedPath": path,
                             "controlToken": control}
                if case == "ipc-target":
                    assertion.update(preExistingTarget=True, requestingRole="excluded")
                if case == "shared-resolver-dns":
                    assertion["attribution"] = "shared-resolver"
                values.append(assertion)
                (tunnel if control_path == "tunnel" else underlay).extend(control.encode())
                if path != "none":
                    (tunnel if path == "tunnel" else underlay).extend(token.encode())
        return values, bytes(tunnel), bytes(underlay)

    def run_validator(self, observations=None, tunnel=None, underlay=None, stale=False):
        if observations is None:
            observations, default_tunnel, default_underlay = self.observations()
            tunnel = default_tunnel if tunnel is None else tunnel
            underlay = default_underlay if underlay is None else underlay
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "observations.jsonl"
            if isinstance(observations, str):
                source.write_text(observations, encoding="utf-8")
            else:
                source.write_text("".join(json.dumps(item) + "\n" for item in observations),
                                  encoding="utf-8")
            (root / "tunnel.pcap").write_bytes(tunnel or b"")
            (root / "underlay.pcap").write_bytes(underlay or b"")
            output = root / "evidence.json"
            if stale:
                output.write_text('[{"token":"STALE"}]', encoding="utf-8")
            result = subprocess.run(
                [sys.executable, SCRIPT, "--observations", source, "--tunnel",
                 root / "tunnel.pcap", "--underlay", root / "underlay.pcap",
                 "--output", output], capture_output=True, text=True, check=False,
            )
            evidence = json.loads(output.read_text()) if output.exists() else None
            return result, evidence

    def test_complete_matrix_passes(self):
        result, evidence = self.run_validator()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(evidence)
        self.assertTrue(all(item["packetPathValid"] for item in evidence))

    def test_backend_public_and_underlay_lifecycle_matrix_passes(self):
        values, tunnel, underlay = [], bytearray(), bytearray()
        sequence = 0
        for case, path in FLOW.BACKEND_PUBLIC_CASES.items():
            for profile, networks in FLOW.PROFILES.items():
                if case == "E05-explicit-application-dns-underlay":
                    networks = {network for network in networks if network.startswith("udp")}
                elif case == "E06-shared-windows-dns-client":
                    networks = ({"ip4"} if profile == "ipv4" else {"ip6"}
                                if profile == "ipv6" else {"ip4", "ip6"})
                for network in sorted(networks):
                    sequence += 1
                    control = f"BACKEND_CONTROL_{sequence:04d}"
                    token = f"BACKEND_ASSERT_{sequence:04d}"
                    control_path = "underlay" if path == "tunnel" else "tunnel"
                    values.append({"case": f"CTRL-{sequence}", "token": control,
                                   "expectedPath": control_path,
                                   "positiveControl": True, "phase": "public-api"})
                    assertion = {"case": case, "token": token, "expectedPath": path,
                                 "profile": profile, "network": network,
                                 "controlToken": control, "phase": "public-api"}
                    if case == "E04-pre-existing-ipc-target-tunnel":
                        assertion.update(preExistingTarget=True,
                                         requestingRole="excluded")
                    if case == "E06-shared-windows-dns-client":
                        assertion["limitation"] = "shared resolver attribution"
                    values.append(assertion)
                    (tunnel if control_path == "tunnel" else underlay).extend(control.encode())
                    (tunnel if path == "tunnel" else underlay).extend(token.encode())
        for case in sorted(FLOW.BACKEND_REQUIRED_CASES):
            token = "BACKEND_" + case.replace("-", "_")
            path = "underlay" if case == "N24-underlay-recovery" else "none"
            values.append({"case": case, "token": token, "expectedPath": path})
            if path == "underlay":
                underlay.extend(token.encode())
        result, evidence = self.run_validator(values, bytes(tunnel), bytes(underlay))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(all(item["packetPathValid"] for item in evidence))

    def test_missing_wrong_and_both_link_markers_fail(self):
        values, tunnel, underlay = self.observations()
        assertion = next(item for item in values if item.get("expectedPath") == "tunnel"
                         and not item.get("positiveControl"))
        marker = assertion["token"].encode()
        for name, changed_tunnel, changed_underlay in (
            ("missing", tunnel.replace(marker, b""), underlay),
            ("wrong", tunnel.replace(marker, b""), underlay + marker),
            ("both", tunnel, underlay + marker),
        ):
            with self.subTest(name=name):
                result, evidence = self.run_validator(values, changed_tunnel, changed_underlay)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("expected tunnel", result.stderr)
                self.assertTrue(any(not item["packetPathValid"] for item in evidence))

    def test_missing_profile_case_or_control_fails(self):
        values, tunnel, underlay = self.observations()
        for name, changed in (
            ("coverage", values[:-2]),
            ("control", values[1:]),
            ("profile", [dict(item, profile="bad") if item.get("case") == "included"
                         else item for item in values]),
        ):
            with self.subTest(name=name):
                result, _ = self.run_validator(changed, tunnel, underlay)
                self.assertNotEqual(result.returncode, 0)

    def test_shared_resolver_cannot_claim_application_attribution(self):
        values, tunnel, underlay = self.observations()
        changed = [dict(item, attribution="requesting-app")
                   if item.get("case") == "shared-resolver-dns" else item for item in values]
        result, evidence = self.run_validator(changed, tunnel, underlay, stale=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIsNone(evidence)
        self.assertIn("attributed to an application", result.stderr)

    def test_malformed_or_overlapping_tokens_remove_stale_evidence(self):
        for observations in (
            "{not-json}\n",
            [{"token": "TOKEN", "expectedPath": "none", "case": "control-1",
              "positiveControl": True},
             {"token": "TOKEN_LONG", "expectedPath": "none", "case": "control-2",
              "positiveControl": True}],
        ):
            result, evidence = self.run_validator(observations, stale=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIsNone(evidence)


if __name__ == "__main__":
    unittest.main()
