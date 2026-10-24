#!/usr/bin/env python3
"""Serve a web-demo artifact directory below an optional URL path."""

import argparse
import http.server
import posixpath
from urllib.parse import urlsplit, urlunsplit


def normalized_base(value: str) -> str:
    # just recipe arguments are positional. Accept the documented key-like form
    # so `just serve-webdemo base_path=almagest` remains self-explanatory.
    if value.startswith("base_path="):
        value = value.removeprefix("base_path=")
    value = "/" + value.strip("/")
    return "/" if value == "/" else value + "/"


def handler_type(directory: str, base: str):
    class Handler(http.server.SimpleHTTPRequestHandler):
        def __init__(self, *args, **kwargs):
            super().__init__(*args, directory=directory, **kwargs)

        def send_head(self):
            parsed = urlsplit(self.path)
            if base != "/" and parsed.path == base.removesuffix("/"):
                self.send_response(301)
                self.send_header("Location", urlunsplit(("", "", base, parsed.query, parsed.fragment)))
                self.end_headers()
                return None
            if not parsed.path.startswith(base):
                self.send_error(404, "Path is outside the configured web-demo base")
                return None
            relative = parsed.path[len(base) :]
            safe_path = "/" + posixpath.normpath("/" + relative).lstrip("/")
            if parsed.path.endswith("/") and not safe_path.endswith("/"):
                safe_path += "/"
            self.path = urlunsplit(("", "", safe_path, parsed.query, parsed.fragment))
            return super().send_head()

    return Handler


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--directory", required=True)
    parser.add_argument("--base-path", default="")
    parser.add_argument("--port", type=int, default=8000)
    parser.add_argument("--host", default="0.0.0.0")
    args = parser.parse_args()
    base = normalized_base(args.base_path)
    server = http.server.ThreadingHTTPServer((args.host, args.port), handler_type(args.directory, base))
    display_host = "127.0.0.1" if args.host == "0.0.0.0" else args.host
    print(f"Serving web demo at http://{display_host}:{args.port}{base}", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
