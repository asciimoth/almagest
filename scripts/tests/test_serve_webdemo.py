import importlib.util
import http.server
import pathlib
import subprocess
import tempfile
import threading
import unittest
import urllib.error
import urllib.request
from unittest import mock

REPOSITORY = pathlib.Path(__file__).resolve().parents[2]
SERVER_SCRIPT = REPOSITORY / "scripts" / "serve-webdemo.py"


def load_server_module():
    spec = importlib.util.spec_from_file_location("serve_webdemo", SERVER_SCRIPT)
    if spec is None or spec.loader is None:
        raise RuntimeError("cannot load serve-webdemo.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class ServeWebdemoTests(unittest.TestCase):
    def serve(self, base):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = pathlib.Path(temporary.name)
        (root / "index.html").write_text("landing", encoding="utf-8")
        (root / "docs").mkdir()
        (root / "docs" / "index.html").write_text("docs", encoding="utf-8")
        module = load_server_module()
        server = http.server.ThreadingHTTPServer(
            ("127.0.0.1", 0), module.handler_type(str(root), base)
        )
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(thread.join, 2)
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        return f"http://127.0.0.1:{server.server_port}"

    def get(self, url):
        try:
            with urllib.request.urlopen(url) as response:
                return response.status, response.geturl(), response.read().decode()
        except urllib.error.HTTPError as error:
            try:
                return error.code, error.geturl(), error.read().decode()
            finally:
                error.close()

    def test_documented_base_path_argument_maps_to_url_prefix(self):
        module = load_server_module()
        self.assertEqual(module.normalized_base("base_path=almagest"), "/almagest/")
        self.assertEqual(module.normalized_base("almagest"), "/almagest/")
        self.assertEqual(module.normalized_base("/almagest/preview/"), "/almagest/preview/")
        self.assertEqual(module.normalized_base(""), "/")

        result = subprocess.run(
            ["just", "--dry-run", "serve-webdemo", "base_path=almagest"],
            cwd=REPOSITORY,
            check=True,
            capture_output=True,
            text=True,
        )
        output = result.stdout + result.stderr
        self.assertIn("just build-webdemo 'build/webdemo'", output)
        self.assertIn("--base-path 'base_path=almagest'", output)
        self.assertIn("--host '0.0.0.0'", output)
        self.assertNotIn("build-webdemo 'base_path=almagest'", output)

    def test_wildcard_host_suggests_local_and_host_urls(self):
        module = load_server_module()
        with mock.patch.object(
            module, "host_ipv4_addresses", return_value=["192.168.1.16", "192.168.2.100"]
        ):
            self.assertEqual(
                module.suggested_urls("0.0.0.0", 8000, "/almagest/"),
                [
                    "http://127.0.0.1:8000/almagest/",
                    "http://192.168.1.16:8000/almagest/",
                    "http://192.168.2.100:8000/almagest/",
                ],
            )

    def test_explicit_host_only_suggests_that_host(self):
        module = load_server_module()
        self.assertEqual(
            module.suggested_urls("127.0.0.1", 8000, "/"),
            ["http://127.0.0.1:8000/"],
        )

    def test_root_hosting_serves_landing_and_nested_static_pages(self):
        origin = self.serve("/")

        self.assertEqual(self.get(origin + "/"), (200, origin + "/", "landing"))
        self.assertEqual(
            self.get(origin + "/docs/?page=guide%2Fwelcome"),
            (200, origin + "/docs/?page=guide%2Fwelcome", "docs"),
        )

    def test_subpath_hosting_redirects_base_and_rejects_outside_paths(self):
        origin = self.serve("/almagest/")

        self.assertEqual(
            self.get(origin + "/almagest?locale=en"),
            (200, origin + "/almagest/?locale=en", "landing"),
        )
        self.assertEqual(self.get(origin + "/almagest/"), (200, origin + "/almagest/", "landing"))
        self.assertEqual(self.get(origin + "/almagest/docs/"), (200, origin + "/almagest/docs/", "docs"))
        self.assertEqual(self.get(origin + "/")[0], 404)
        self.assertEqual(self.get(origin + "/almagest-other/")[0], 404)


if __name__ == "__main__":
    unittest.main()
