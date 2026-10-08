"""Exercise the real QGA client with private fake sockets."""

import base64
import json
from pathlib import Path
import socket
import subprocess
import tempfile
import threading
import unittest

CLIENT = Path(__file__).resolve().parents[1] / 'tools/qga.py'


class AgentTests(unittest.TestCase):
    def run_client(self, mode, arguments, timeout='.5'):
        with tempfile.TemporaryDirectory() as temp:
            path = str(Path(temp) / 'agent.sock')
            server = socket.socket(socket.AF_UNIX)
            server.bind(path)
            server.listen()
            server.settimeout(0.1)
            stop = threading.Event()

            def serve():
                while not stop.is_set():
                    try:
                        conn, _ = server.accept()
                    except socket.timeout:
                        continue
                    with conn:
                        conn.settimeout(1)
                        stream = conn.makefile('rb')
                        if mode == 'qmp':
                            conn.sendall(b'{"QMP":{}}\n')
                        request = json.loads(stream.readline())
                        command = request['execute']
                        if command == 'qmp_capabilities':
                            conn.sendall(b'{"return":{}}\n')
                            stream.readline()
                        response = {}
                        if command == 'guest-exec':
                            response = {'pid': 7}
                        elif command == 'guest-exec-status':
                            response = {'exited': mode != 'timeout', 'exitcode': 7}
                            if mode == 'truncated':
                                response['out-truncated'] = True
                        elif command == 'guest-file-open':
                            response = 1
                        elif command == 'guest-file-read':
                            response = {'buf-b64': base64.b64encode(b'hello').decode(), 'eof': True}
                        conn.sendall(json.dumps({'return': response}).encode() + b'\n')
                        stream.close()

            thread = threading.Thread(target=serve, daemon=True)
            thread.start()
            try:
                return subprocess.run([str(CLIENT), '--socket', path, '--timeout', timeout,
                                       *arguments], capture_output=True, timeout=3)
            finally:
                stop.set()
                thread.join(2)
                server.close()

    def test_ping_and_qmp(self):
        self.assertEqual(self.run_client('ping', ['ping']).returncode, 0)
        self.assertEqual(self.run_client('qmp', ['qmp', 'query-status']).returncode, 0)

    def test_guest_exit_status(self):
        self.assertEqual(self.run_client('exit', ['exec', 'cmd.exe']).returncode, 7)

    def test_zero_timeout_allows_unbounded_guest_exec(self):
        self.assertEqual(self.run_client('exit', ['exec', 'cmd.exe'], timeout='0').returncode, 7)

    def test_timeout(self):
        result = self.run_client('timeout', ['exec', 'cmd.exe'])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(b'did not finish', result.stderr)

    def test_truncated_output_is_failure(self):
        self.assertNotEqual(self.run_client('truncated', ['exec', 'cmd.exe']).returncode, 0)

    def test_read_bounds(self):
        self.assertEqual(self.run_client('read', ['read', 'C:\\fixture']).stdout, b'hello')
        self.assertNotEqual(self.run_client('read', ['read', 'C:\\fixture', '--limit', '4']).returncode, 0)
