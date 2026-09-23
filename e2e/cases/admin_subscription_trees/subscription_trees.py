#!/usr/bin/env python3
import json
import select
import socket
import sys
import time


class API:
    def __init__(self, path):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.connect(path)
        self.buffer = bytearray()
        self.next_id = 0
        self.events = []
        self._read_line()  # Initial project_version_status event.

    def _read_line(self):
        while b"\n" not in self.buffer:
            data = self.sock.recv(65536)
            if not data:
                raise AssertionError("admin API connection closed")
            self.buffer.extend(data)
        line, _, remainder = self.buffer.partition(b"\n")
        self.buffer = bytearray(remainder)
        return json.loads(line)

    def request(self, surface, method, payload=None):
        self.next_id += 1
        request_id = str(self.next_id)
        message = {
            "version": "v1",
            "kind": "request",
            "id": request_id,
            "surface": surface,
            "method": method,
        }
        if payload is not None:
            message["payload"] = payload
        self.sock.sendall((json.dumps(message) + "\n").encode())
        while True:
            response = self._read_line()
            if response.get("kind") == "event":
                self.events.append(response)
                continue
            if response.get("id") != request_id:
                raise AssertionError(f"unexpected response: {response}")
            if response.get("error"):
                raise AssertionError(f"request failed: {response['error']}")
            return response.get("result", {})

    def drain_events(self, timeout=0.5):
        deadline = time.monotonic() + timeout
        while True:
            if b"\n" in self.buffer:
                message = self._read_line()
                if message.get("kind") != "event":
                    raise AssertionError(f"unexpected asynchronous response: {message}")
                self.events.append(message)
                continue
            remaining = deadline - time.monotonic()
            if remaining <= 0 or not select.select([self.sock], [], [], remaining)[0]:
                return
            message = self._read_line()
            if message.get("kind") != "event":
                raise AssertionError(f"unexpected asynchronous response: {message}")
            self.events.append(message)

    def event_methods(self, surface):
        return [
            event["method"]
            for event in self.events
            if event.get("surface") == surface
        ]


api = API(sys.argv[1])

plugin = {"kind": "plugin", "name": "subscription-test"}
api.request("kv", "ensure", {"store": plugin, "path": ["branch"]})
kv_stores = api.request("kv", "subscribe_stores")["subscription_id"]
kv_entries = api.request(
    "kv", "subscribe_entries", {"store": plugin, "path": ["branch"]}
)["subscription_id"]
api.request(
    "kv",
    "subscribe_value",
    {"store": plugin, "path": ["branch"], "key": "leaf"},
)
api.request("kv", "unsubscribe", {"subscription_id": kv_entries})
api.request(
    "kv",
    "set",
    {
        "store": {"kind": "plugin", "name": "another-store"},
        "key": "root-value",
        "value": {"data": "created", "encoding": "string", "string": True},
    },
)
api.drain_events()
if api.event_methods("kv") != ["stores_update"]:
    raise AssertionError(f"unexpected KV events after subtree removal: {api.events}")

api.events.clear()
api.request("kv", "unsubscribe", {"subscription_id": kv_stores})
api.request("kv", "unsubscribe", {"subscription_id": kv_stores})
api.request(
    "kv",
    "set",
    {
        "store": {"kind": "plugin", "name": "third-store"},
        "key": "root-value",
        "value": {"data": "created", "encoding": "string", "string": True},
    },
)
api.drain_events()
if api.event_methods("kv"):
    raise AssertionError(f"KV event arrived after root removal: {api.events}")

initial_dns = api.request(
    "dns",
    "set",
    {
        "cache": "subscription-cache",
        "record": {
            "name": "initial.test.",
            "type": "A",
            "class": "IN",
            "ttl": 3600,
            "value": "192.0.2.1",
        },
    },
)
dns_key = initial_dns["key"]
dns_caches = api.request("dns", "subscribe_caches")["subscription_id"]
dns_keys = api.request(
    "dns", "subscribe_keys", {"cache": "subscription-cache"}
)["subscription_id"]
api.request(
    "dns",
    "subscribe_value",
    {"cache": "subscription-cache", "key": dns_key},
)
api.request("dns", "unsubscribe", {"subscription_id": dns_keys})
api.request(
    "dns",
    "set",
    {
        "cache": "second-cache",
        "record": {
            "name": "second.test.",
            "type": "A",
            "class": "IN",
            "ttl": 3600,
            "value": "192.0.2.2",
        },
    },
)
api.drain_events()
if api.event_methods("dns") != ["caches_update"]:
    raise AssertionError(f"unexpected DNS events after subtree removal: {api.events}")

api.events.clear()
api.request("dns", "unsubscribe", {"subscription_id": dns_caches})
api.request("dns", "unsubscribe", {"subscription_id": dns_caches})
api.request(
    "dns",
    "set",
    {
        "cache": "third-cache",
        "record": {
            "name": "third.test.",
            "type": "A",
            "class": "IN",
            "ttl": 3600,
            "value": "192.0.2.3",
        },
    },
)
api.drain_events()
if api.event_methods("dns"):
    raise AssertionError(f"DNS event arrived after root removal: {api.events}")

for surface in ("secrets", "tls"):
    subscription_id = api.request(surface, "subscribe")["subscription_id"]
    api.request(surface, "unsubscribe", {"subscription_id": subscription_id})
    api.request(surface, "unsubscribe", {"subscription_id": subscription_id})
