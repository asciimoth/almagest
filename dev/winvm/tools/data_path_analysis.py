#!/usr/bin/env python3
"""Summarize Windows data-path evidence from the WinVM harness."""

import argparse
import json
from pathlib import Path
import statistics
import tarfile


BUDGETS = {
    "linux": {"latency_p50_ns": 250_000, "latency_p99_ns": 1_000_000,
              "tun_p99_ns": 500_000, "goodput_loss": 0.10,
              "pps_loss": 0.15, "cpu_increase": 0.20, "control_p99_ns": 100_000_000},
    "windows": {"latency_p50_ns": 500_000, "latency_p99_ns": 2_000_000,
                "tun_p99_ns": 1_000_000, "goodput_loss": 0.15,
                "pps_loss": 0.20, "cpu_increase": 0.25, "control_p99_ns": 150_000_000},
}


def load_documents(paths):
    documents, sources = [], []
    for path in paths:
        if path.name.endswith(".tar"):
            with tarfile.open(path) as archive:
                members = [member for member in archive.getmembers()
                           if member.isfile()
                           and "/windows-isolation-" in member.name
                           and Path(member.name).name in {
                               "result.json", "result-disabled.json", "result-overloaded.json"}]
                for member in sorted(members, key=lambda value: value.name):
                    documents.append(json.load(archive.extractfile(member)))
                    sources.append(f"{path}:{member.name}")
        else:
            documents.append(json.loads(path.read_text()))
            sources.append(str(path))
    return documents, sources


def median(items, field):
    values = [item[field] for item in items if item.get("temperature") == "warm"]
    return statistics.median(values) if values else None


def key(item):
    return tuple(item.get(name) for name in
                 ("transport", "family", "workload", "packet_bytes", "batch_size"))


def normalize(item):
    matcher_flow = item.get("flow")
    return {
        "candidate": item.get("candidate"),
        "profile": item.get("_evidence_profile"),
        "log_mode": item.get("_log_mode"),
        "transport": item.get("transport", "matcher" if matcher_flow else "tun"),
        "family": item.get("family", matcher_flow),
        "workload": item.get("workload", "plain"),
        "packet_bytes": item.get("packet_bytes", item.get("packetBytes", 0)),
        "batch_size": item.get("batch_size", item.get("batchSize", 1)),
        "run": item.get("run"),
        "temperature": item.get("temperature"),
        "goodput": item.get("goodput_bits_second", item.get("goodputBitsSecond",
                    item.get("packets_second", item.get("lookups_second", 0)))),
        "p50_ns": item.get("p50_ns", item.get("p50NS", 0)),
        "p99_ns": item.get("p99_ns", item.get("p99NS", 0)),
        "cpu_ns": item.get("runtime_cpu_ns", item.get("runtimeCpuNS", 0)) +
                  item.get("broker_cpu_ns", item.get("brokerCpuNS", 0)),
        "dropped_packets": item.get("dropped_packets", item.get("droppedPackets", 0)),
    }


def compare(records, platform):
    budget = BUDGETS[platform]
    groups = {}
    for raw in records:
        item = normalize(raw)
        context = (item["profile"], item["log_mode"])
        groups.setdefault((item["candidate"], context, key(item)), []).append(item)
    comparisons = []
    for (candidate, context, workload_key), items in groups.items():
        if candidate.startswith("in-process-native"):
            continue
        baseline_names = (["in-process-native-matcher", "in-process-native"]
                          if candidate == "bounded-matcher-rpc" else ["in-process-native"])
        baseline = next((groups.get((name, context, workload_key)) for name in baseline_names
                         if groups.get((name, context, workload_key))), None)
        if not baseline:
            # Batched candidates compare with the corresponding one-record baseline.
            base_key = list(workload_key)
            base_key[-1] = 1
            baseline = next((groups.get((name, context, tuple(base_key))) for name in baseline_names
                             if groups.get((name, context, tuple(base_key)))), None)
        if not baseline:
            continue
        candidate_p50, baseline_p50 = median(items, "p50_ns"), median(baseline, "p50_ns")
        candidate_p99, baseline_p99 = median(items, "p99_ns"), median(baseline, "p99_ns")
        candidate_rate, baseline_rate = median(items, "goodput"), median(baseline, "goodput")
        candidate_cpu, baseline_cpu = median(items, "cpu_ns"), median(baseline, "cpu_ns")
        added_p50, added_p99 = candidate_p50 - baseline_p50, candidate_p99 - baseline_p99
        loss = 1 - candidate_rate / baseline_rate if baseline_rate else None
        cpu_increase = candidate_cpu / baseline_cpu - 1 if baseline_cpu and candidate_cpu else None
        warm_drops = sum(item["dropped_packets"] for item in items
                         if item.get("temperature") == "warm")
        limit = budget["pps_loss"] if workload_key[0] == "tun" and workload_key[3] == 64 else budget["goodput_loss"]
        p99_limit = budget["tun_p99_ns"] if workload_key[0] == "tun" else budget["latency_p99_ns"]
        comparisons.append({
            "candidate": candidate, "profile": context[0], "log_mode": context[1],
            "workload": workload_key,
            "added_p50_ns": added_p50, "added_p99_ns": added_p99,
            "rate_loss": loss, "cpu_increase": cpu_increase,
            "warm_dropped_packets": warm_drops,
            "packet_loss_budget_pass": warm_drops == 0,
            "median_latency_budget_pass": (None if workload_key[0] in ("tun", "matcher")
                                             else added_p50 <= budget["latency_p50_ns"]),
            "p99_latency_budget_pass": added_p99 <= p99_limit,
            "rate_budget_pass": loss is not None and loss <= limit + 1e-12,
            "cpu_budget_pass": (None if cpu_increase is None
                                else cpu_increase <= budget["cpu_increase"]),
        })
    return comparisons


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("platform", choices=BUDGETS)
    parser.add_argument("inputs", nargs="+", type=Path)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    documents, sources = load_documents(args.inputs)
    measurements = []
    controls = []
    for document in documents:
        data = document.get("dataPath", document)
        profile = document.get("profile", data.get("profile"))
        log_mode = data.get("log_mode", data.get("logMode"))
        for measurement in data.get("measurements", []):
            enriched = dict(measurement)
            enriched["_evidence_profile"] = profile
            enriched["_log_mode"] = log_mode
            measurements.append(enriched)
        if "control_response_p99_ns" in data:
            controls.append(data["control_response_p99_ns"])
        elif "controlResponseP99NS" in data:
            controls.append(data["controlResponseP99NS"])
    result = {
        "schema_version": 1,
        "platform": args.platform,
        "inputs": sources,
        "comparisons": compare(measurements, args.platform),
        "worst_control_p99_ns": max(controls) if controls else None,
        "control_budget_pass": bool(controls) and max(controls) <= BUDGETS[args.platform]["control_p99_ns"],
    }
    output = json.dumps(result, indent=2) + "\n"
    if args.output:
        args.output.write_text(output)
    else:
        print(output, end="")


if __name__ == "__main__":
    main()
