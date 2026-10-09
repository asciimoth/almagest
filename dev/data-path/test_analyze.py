#!/usr/bin/env python3

import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest


MODULE_PATH = Path(__file__).with_name("analyze.py")
SPEC = importlib.util.spec_from_file_location("data_path_analyze", MODULE_PATH)
ANALYZE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ANALYZE)


class AnalyzeTests(unittest.TestCase):
    def test_loads_windows_results_from_vm_archive(self):
        temporary = tempfile.NamedTemporaryFile(suffix=".tar", delete=False)
        temporary.close()
        self.addCleanup(Path(temporary.name).unlink)
        document = {"profile": "service", "dataPath": {"measurements": []}}
        data = json.dumps(document).encode()
        with tarfile.open(temporary.name, "w") as archive:
            member = tarfile.TarInfo(
                "artifacts/windows-isolation-service/result-disabled.json")
            member.size = len(data)
            archive.addfile(member, io.BytesIO(data))

        documents, sources = ANALYZE.load_documents([Path(temporary.name)])

        self.assertEqual(documents, [document])
        self.assertIn("result-disabled.json", sources[0])

    def test_linux_matcher_records_keep_each_flow_separate(self):
        records = []
        for flow in ("tcp4", "udp6"):
            records.extend([
                {"candidate": "in-process-native", "flow": flow, "run": 1,
                 "temperature": "warm", "lookups_second": 1000,
                 "p50_ns": 100, "p99_ns": 200},
                {"candidate": "bounded-matcher-rpc", "flow": flow, "run": 1,
                 "temperature": "warm", "lookups_second": 900,
                 "p50_ns": 200, "p99_ns": 300},
            ])

        comparisons = ANALYZE.compare(records, "linux")

        self.assertEqual(len(comparisons), 2)
        self.assertEqual({item["workload"][1] for item in comparisons}, {"tcp4", "udp6"})
        self.assertTrue(all(item["rate_budget_pass"] for item in comparisons))
        self.assertTrue(all(item["cpu_budget_pass"] is None for item in comparisons))

    def test_tun_uses_packet_rate_and_tun_latency_budget(self):
        records = [
            {"candidate": "in-process-native", "family": "ipv4", "workload": "plain",
             "packet_bytes": 64, "batch_size": 1, "temperature": "warm",
             "packets_second": 1000, "p50_ns": 100, "p99_ns": 100},
            {"candidate": "broker-relay", "family": "ipv4", "workload": "plain",
             "packet_bytes": 64, "batch_size": 1, "temperature": "warm",
             "packets_second": 850, "p50_ns": 200, "p99_ns": 500101},
        ]

        comparison = ANALYZE.compare(records, "linux")[0]

        self.assertAlmostEqual(comparison["rate_loss"], 0.15)
        self.assertTrue(comparison["rate_budget_pass"])
        self.assertFalse(comparison["p99_latency_budget_pass"])

    def test_packet_drop_fails_loss_budget(self):
        records = [
            {"candidate": "in-process-native", "transport": "tun", "family": "ipv4",
             "workload": "plain", "packet_bytes": 64, "batch_size": 1,
             "temperature": "warm", "packets_second": 1000, "p50_ns": 100, "p99_ns": 100},
            {"candidate": "broker-relay", "transport": "tun", "family": "ipv4",
             "workload": "plain", "packet_bytes": 64, "batch_size": 1,
             "temperature": "warm", "packets_second": 999, "p50_ns": 100, "p99_ns": 100,
             "dropped_packets": 1},
        ]

        comparison = ANALYZE.compare(records, "linux")[0]

        self.assertEqual(comparison["warm_dropped_packets"], 1)
        self.assertFalse(comparison["packet_loss_budget_pass"])


if __name__ == "__main__":
    unittest.main()
