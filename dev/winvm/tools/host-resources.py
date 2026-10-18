#!/usr/bin/env python3
"""Record host and selected process resources as fsynced JSON lines."""

import argparse
import datetime
import json
import os
from pathlib import Path
import platform
import signal
import threading
import time


stop = threading.Event()


def read_text(path):
    try:
        return Path(path).read_text(errors="replace")
    except OSError:
        return ""


def integer_fields(path, names):
    values = {}
    for line in read_text(path).splitlines():
        parts = line.replace(":", "").split()
        if len(parts) >= 2 and parts[0] in names:
            try:
                values[parts[0]] = int(parts[1])
            except ValueError:
                pass
    return values


def pressure(kind):
    values = {}
    for line in read_text(f"/proc/pressure/{kind}").splitlines():
        parts = line.split()
        if not parts:
            continue
        values[parts[0]] = {
            key: float(value) if key != "total" else int(value)
            for key, value in (part.split("=", 1) for part in parts[1:])
        }
    return values


def cgroup_values(pid):
    relative = ""
    for line in read_text(f"/proc/{pid}/cgroup").splitlines():
        parts = line.split(":", 2)
        if len(parts) == 3 and parts[:2] == ["0", ""]:
            relative = parts[2].lstrip("/")
            break
    if not relative:
        return {}
    root = Path("/sys/fs/cgroup") / relative
    result = {"path": "/" + relative}
    for name in ("memory.current", "memory.peak", "memory.swap.current",
                 "memory.max", "memory.high", "pids.current"):
        value = read_text(root / name).strip()
        if value:
            result[name] = int(value) if value.isdigit() else value
    events = integer_fields(root / "memory.events", {
        "low", "high", "max", "oom", "oom_kill", "oom_group_kill",
    })
    if events:
        result["memory.events"] = events
    return result


def process_sample(pid, include_command=True):
    status = integer_fields(f"/proc/{pid}/status", {
        "VmPeak", "VmSize", "VmHWM", "VmRSS", "RssAnon", "RssFile",
        "RssShmem", "VmSwap", "Threads",
    })
    stat = read_text(f"/proc/{pid}/stat")
    if not stat:
        return {"pid": pid, "alive": False}
    tail = stat.rsplit(")", 1)
    fields = tail[1].split() if len(tail) == 2 else []
    threads = status.pop("Threads", None)
    result = {"pid": pid, "alive": True, "memoryKiB": status}
    if threads is not None:
        result["threads"] = threads
    if len(fields) > 20:
        result.update(state=fields[0], userTicks=int(fields[11]),
                      systemTicks=int(fields[12]), threads=int(fields[17]),
                      startTicks=int(fields[19]))
    io_values = integer_fields(f"/proc/{pid}/io", {
        "rchar", "wchar", "read_bytes", "write_bytes", "cancelled_write_bytes",
    })
    if io_values:
        result["ioBytes"] = io_values
    if include_command:
        command = read_text(f"/proc/{pid}/cmdline").replace("\0", " ").strip()
        result["command"] = command
        result["cgroup"] = cgroup_values(pid)
    return result


def top_rss(limit=12):
    samples = []
    for entry in Path("/proc").iterdir():
        if not entry.name.isdigit():
            continue
        pid = int(entry.name)
        status = integer_fields(entry / "status", {"VmRSS", "VmSwap", "Threads"})
        if "VmRSS" not in status:
            continue
        samples.append({
            "pid": pid,
            "name": read_text(entry / "comm").strip(),
            "rssKiB": status["VmRSS"],
            "swapKiB": status.get("VmSwap", 0),
            "threads": status.get("Threads", 0),
        })
    return sorted(samples, key=lambda item: item["rssKiB"], reverse=True)[:limit]


def filesystem(path):
    try:
        stat = os.statvfs(path)
    except OSError:
        return {}
    return {
        "bytesTotal": stat.f_blocks * stat.f_frsize,
        "bytesAvailable": stat.f_bavail * stat.f_frsize,
        "inodesAvailable": stat.f_favail,
    }


def sample(targets, filesystem_path):
    memory = integer_fields("/proc/meminfo", {
        "MemTotal", "MemFree", "MemAvailable", "Buffers", "Cached",
        "SwapCached", "Active", "Inactive", "AnonPages", "Mapped",
        "Shmem", "Slab", "SReclaimable", "SUnreclaim", "PageTables",
        "Committed_AS", "SwapTotal", "SwapFree",
    })
    vmstat = integer_fields("/proc/vmstat", {
        "pswpin", "pswpout", "pgscan_kswapd", "pgscan_direct",
        "pgsteal_kswapd", "pgsteal_direct", "oom_kill",
    })
    load = read_text("/proc/loadavg").split()
    cpu = read_text("/proc/stat").splitlines()
    return {
        "timestamp": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "monotonicSeconds": time.monotonic(),
        "host": {
            "memoryKiB": memory,
            "vmstat": vmstat,
            "pressure": {kind: pressure(kind) for kind in ("cpu", "memory", "io")},
            "loadAverage": load[:3],
            "runnableEntities": load[3] if len(load) > 3 else "",
            "cpuStat": cpu[0].split()[1:] if cpu else [],
            "filesystem": filesystem(filesystem_path),
            "topRss": top_rss(),
        },
        "processes": {name: process_sample(pid) for name, pid in targets.items()},
    }


def parse_target(value):
    try:
        name, raw_pid = value.split("=", 1)
        pid = int(raw_pid)
    except (ValueError, TypeError) as error:
        raise argparse.ArgumentTypeError("PID target must have the form NAME=PID") from error
    if not name or pid <= 0:
        raise argparse.ArgumentTypeError("PID target must have the form NAME=PID")
    return name, pid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--filesystem", type=Path,
                        help="filesystem to measure when output is staged elsewhere")
    parser.add_argument("--pid", action="append", default=[], type=parse_target)
    parser.add_argument("--interval", type=float, default=2.0)
    parser.add_argument("--append", action="store_true")
    parser.add_argument("--exit-with-pid", type=int)
    args = parser.parse_args()
    if args.interval <= 0:
        parser.error("--interval must be positive")
    if args.exit_with_pid is not None and args.exit_with_pid <= 0:
        parser.error("--exit-with-pid must be positive")
    targets = dict(args.pid)
    if len(targets) != len(args.pid):
        parser.error("PID target names must be unique")
    args.output.parent.mkdir(parents=True, exist_ok=True)
    exit_start = None
    if args.exit_with_pid is not None:
        exit_start = process_sample(args.exit_with_pid, include_command=False).get("startTicks")
    for number in (signal.SIGINT, signal.SIGTERM):
        signal.signal(number, lambda _number, _frame: stop.set())
    mode = "a" if args.append else "w"
    with args.output.open(mode, buffering=1) as output:
        header = {
            "type": "start",
            "timestamp": datetime.datetime.now(datetime.timezone.utc).isoformat(),
            "kernel": platform.release(),
            "platform": platform.platform(),
            "cpuCount": os.cpu_count(),
            "clockTicksPerSecond": os.sysconf("SC_CLK_TCK"),
            "pageSize": os.sysconf("SC_PAGE_SIZE"),
            "targets": targets,
        }
        output.write(json.dumps(header, separators=(",", ":")) + "\n")
        output.flush()
        os.fsync(output.fileno())
        while True:
            output.write(json.dumps(sample(targets, args.filesystem or args.output.parent), separators=(",", ":")) + "\n")
            output.flush()
            os.fsync(output.fileno())
            if args.exit_with_pid is not None:
                current = process_sample(args.exit_with_pid, include_command=False)
                if not current.get("alive") or current.get("startTicks") != exit_start:
                    break
            if stop.wait(args.interval):
                break
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
