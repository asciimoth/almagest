"""Evidence and deadline invariants that do not require a Windows guest."""

import argparse
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import tarfile
import time
import unittest
import subprocess
from contextlib import redirect_stdout
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("winvm_supervisor", ROOT / "supervise.py")
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class SupervisorTests(unittest.TestCase):
    def supervisor(self, directory):
        args = argparse.Namespace(run_dir=str(directory), sockets=str(directory / "almagest-winvm.fixture"),
                                  key="fixture-key", port=22000, mode="test", qemu=[],
                                  cache_dir=str(directory),
                                  orphan_marker=str(directory / "qemu-orphan"))
        return MODULE.Supervisor(args, json.loads((ROOT / "config.json").read_text())["machine"])

    def test_partial_export_never_replaces_last_good_archive(self):
        with tempfile.TemporaryDirectory() as temp, redirect_stdout(io.StringIO()):
            path = Path(temp)
            supervisor = self.supervisor(path)

            def export(_argv, _seconds, output, **_kwargs):
                with tarfile.open(fileobj=output, mode="w|") as archive:
                    member = tarfile.TarInfo("artifacts/events/lib.jsonl")
                    member.size = 8
                    archive.addfile(member, io.BytesIO(b"evidence"))
                return 0

            supervisor.command = export
            self.assertTrue(supervisor.harvest(final=False))
            good = (path / "guest-artifacts.tar").read_bytes()

            def locked_file(*args, **kwargs):
                export(*args, **kwargs)
                return 1

            supervisor.command = locked_file
            self.assertFalse(supervisor.harvest(final=False))
            self.assertEqual((path / "guest-artifacts.tar").read_bytes(), good)
            self.assertTrue((path / "guest-artifacts.snapshot.tar").exists())

            def partial(_argv, _seconds, output, **_kwargs):
                output.write(b"broken archive")
                return 124

            supervisor.command = partial
            self.assertFalse(supervisor.harvest(final=True))
            self.assertEqual((path / "guest-artifacts.tar").read_bytes(), good)
            self.assertEqual((path / "guest-artifacts.partial.tar").read_bytes(), b"broken archive")
            record = json.loads((path / "collection.json").read_text())
            self.assertFalse(record["finalCollected"])
            self.assertEqual([attempt["exitStatus"] for attempt in record["attempts"]], [0, 1, 124])
            self.assertTrue(record["attempts"][1]["partialSnapshot"])

    def test_socket_cleanup_rejects_broad_or_symlinked_targets(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp)
            supervisor = self.supervisor(path)
            supervisor.args.sockets = str(path)
            with self.assertRaisesRegex(ValueError, "unsafe VM socket"):
                MODULE.Supervisor(supervisor.args, supervisor.config)
            link = path / "almagest-winvm.escape"
            link.symlink_to(path, target_is_directory=True)
            supervisor.args.sockets = str(link)
            with self.assertRaisesRegex(ValueError, "unsafe VM socket"):
                MODULE.Supervisor(supervisor.args, supervisor.config)

    def test_package_builds_use_a_stable_guest_path(self):
        with tempfile.TemporaryDirectory() as temp:
            first = self.supervisor(Path(temp) / "run-package-a")
            second = self.supervisor(Path(temp) / "run-package-b")
            first.args.mode = second.args.mode = "package"
            self.assertEqual(MODULE.Supervisor(first.args, first.config).remote,
                             MODULE.Supervisor(second.args, second.config).remote)
            self.assertNotEqual(first.remote, second.remote)

    def test_cleanup_deadline_still_records_failed_export(self):
        with tempfile.TemporaryDirectory() as temp, redirect_stdout(io.StringIO()):
            path = Path(temp)
            supervisor = self.supervisor(path)
            supervisor.cleaning = True
            supervisor.deadline = time.monotonic() - 1
            with self.assertRaisesRegex(MODULE.Failure, "cleanup-timeout"):
                supervisor.harvest(final=True)
            record = json.loads((path / "collection.json").read_text())
            self.assertFalse(record["finalCollected"])
            self.assertEqual(record["attempts"][-1]["exitStatus"], 124)

    def test_cpu_mitigation_applies_to_builder_and_runner_and_image_identity(self):
        config = json.loads((ROOT / "config.json").read_text())
        self.assertEqual(config["machine"]["cpu"], "host,cet-ss=off,cet-ibt=off")
        for name in ("run.sh", "build-image.sh"):
            self.assertIn('-cpu "$(jq -r .machine.cpu "$config_file")"', (ROOT / name).read_text())
        common = (ROOT / "common.sh").read_text()
        self.assertIn('files=("$config_file"', common)

    def test_shutdown_reaps_vm_even_if_transport_cannot_start_and_console_closed(self):
        with tempfile.TemporaryDirectory() as temp:
            supervisor = self.supervisor(Path(temp))
            supervisor.qemu = subprocess.Popen(["sleep", "120"], start_new_session=True)
            try:
                with patch.object(supervisor, "command", side_effect=OSError("transport absent")), \
                     patch("builtins.print", side_effect=BrokenPipeError):
                    self.assertFalse(supervisor.stop_vm())
                self.assertIsNotNone(supervisor.qemu.poll())
            finally:
                if supervisor.qemu.poll() is None:
                    supervisor.qemu.kill()
                    supervisor.qemu.wait(timeout=3)
