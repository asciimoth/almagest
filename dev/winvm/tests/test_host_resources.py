"""Tests for the crash-safe host resource recorder."""

import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest


SCRIPT = Path(__file__).resolve().parents[1] / "tools/host-resources.py"


class HostResourceTests(unittest.TestCase):
    def test_monitor_flushes_header_and_samples(self):
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp) / "host-resources.jsonl"
            process = subprocess.Popen([
                sys.executable, str(SCRIPT), "--output", str(output),
                "--interval", "0.05", "--pid", f"test={os.getpid()}",
            ])
            deadline = time.monotonic() + 5
            while (not output.exists() or output.stat().st_size == 0) and time.monotonic() < deadline:
                time.sleep(0.01)
            process.send_signal(signal.SIGTERM)
            process.wait(timeout=5)
            self.assertEqual(process.returncode, 0)
            records = [json.loads(line) for line in output.read_text().splitlines()]
            self.assertGreaterEqual(len(records), 2)
            self.assertEqual(records[0]["type"], "start")
            sample = records[1]
            self.assertIn("memoryKiB", sample["host"])
            self.assertIn("pressure", sample["host"])
            self.assertIn("topRss", sample["host"])
            self.assertTrue(sample["processes"]["test"]["alive"])

    def test_monitor_exits_with_watched_process(self):
        with tempfile.TemporaryDirectory() as temp:
            watched = subprocess.Popen(["sleep", "0.1"])
            process = subprocess.Popen([
                sys.executable, str(SCRIPT), "--output", str(Path(temp) / "resources.jsonl"),
                "--interval", "0.02", "--exit-with-pid", str(watched.pid),
            ])
            watched.wait(timeout=2)
            process.wait(timeout=2)
            self.assertEqual(process.returncode, 0)


if __name__ == "__main__":
    unittest.main()
