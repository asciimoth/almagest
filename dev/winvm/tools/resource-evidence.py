#!/usr/bin/env python3
"""Validate a Windows resource-gate artifact independently of the guest."""

import argparse
import json
import tarfile


PREFIX = "artifacts/windows-m8-resource/"
EXPECTED_CASES = {
    "backend-lifecycle-cancellation-soak",
    "daemon-graph-and-forced-restart",
    "fault-injection-recovery-contracts",
    "fault-injection-split-recovery-contracts",
}
EXPECTED_FAULTS = {
    "dns-external-edit",
    "adapter-name-reuse",
    "busy-close-and-failed-reset",
    "zombie-or-changed-driver",
}
EXPECTED_BUDGETS = {
    "maximumHandleGrowth": 8,
    "maximumGoroutineGrowth": 4,
    "maximumPrivateByteGrowth": 67108864,
}


def read_json(archive, name):
    member = archive.extractfile(PREFIX + name)
    if member is None:
        raise ValueError(f"resource artifact is absent: {name}")
    return json.loads(member.read().decode("utf-8-sig"))


def validate(path, allow_diagnostic=False):
    with tarfile.open(path) as archive:
        suite = read_json(archive, "suite.json")
        before = read_json(archive, "resources-before.json")
        after = read_json(archive, "resources-after.json")
        report = read_json(archive, "process-resources.json")

    if before != after:
        raise ValueError("native adapter, address, route, or DNS state changed")
    if suite.get("schemaVersion") != 1 or suite.get("suite") != "windows-m8-resource":
        raise ValueError("resource suite identity is invalid")
    if suite.get("outcome") != "passed" or suite.get("platformId") != "windows-server-2022-amd64":
        raise ValueError("resource suite did not pass on the qualified platform")
    workload = suite.get("workload", {})
    expected_workload = {
        "warmupCycles": 10, "batchCount": 4, "cyclesPerBatch": 25,
        "cancellationCycles": 100,
    }
    if any(workload.get(key) != value for key, value in expected_workload.items()):
        raise ValueError("resource workload does not match the fixed qualification workload")
    diagnostic = workload.get("diagnosticOverride") is True
    if diagnostic != (suite.get("qualifying") is False):
        raise ValueError("diagnostic and qualification labels disagree")
    if diagnostic:
        if not allow_diagnostic:
            raise ValueError("a shortened diagnostic soak cannot qualify")
    elif workload.get("soakDuration") != "30m" or suite.get("qualifying") is not True:
        raise ValueError("the qualifying resource soak must run for 30 minutes")
    if suite.get("settlingSeconds") != 10 or suite.get("budgets") != EXPECTED_BUDGETS:
        raise ValueError("resource budgets or settling window changed")
    if set(suite.get("cases", {})) != EXPECTED_CASES or any(
            result != "passed" for result in suite["cases"].values()):
        raise ValueError("a required resource case is absent or did not pass")
    faults = suite.get("failureInjection", [])
    if {entry.get("case") for entry in faults} != EXPECTED_FAULTS or any(
            entry.get("method") != "fault-injection" or not entry.get("retained") or not entry.get("recovery")
            for entry in faults):
        raise ValueError("failure-injection retention evidence is incomplete")
    journal = suite.get("recoveryJournal", {})
    if journal.get("persistentCrossProcessSplitJournal") is not False or not journal.get("manualRecovery"):
        raise ValueError("split recovery limitations are not recorded")

    if report.get("warmupCycles") != 10 or report.get("batchCount") != 4 or report.get("cyclesPerBatch") != 25:
        raise ValueError("process-resource report workload is invalid")
    samples = report.get("samples", [])
    if len(samples) != 5:
        raise ValueError("process-resource sample count is invalid")
    first, last = samples[0], samples[-1]
    growth = {
        "maximumHandleGrowth": last["handles"] - first["handles"],
        "maximumGoroutineGrowth": last["goroutines"] - first["goroutines"],
        "maximumPrivateByteGrowth": last["privateBytes"] - first["privateBytes"],
    }
    for name, value in growth.items():
        if value > EXPECTED_BUDGETS[name]:
            raise ValueError(f"{name} exceeded: {value}")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("artifact")
    parser.add_argument("--allow-diagnostic", action="store_true")
    args = parser.parse_args()
    validate(args.artifact, args.allow_diagnostic)


if __name__ == "__main__":
    main()
