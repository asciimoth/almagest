# Daemon isolation performance policy

Status: current performance accepted on 2026-10-11.

Performance is not an isolation implementation, qualification, release, or
default-enable gate. The project accepts the current Linux and Windows backend,
relay, matcher, control-path, direct-mode, CPU, memory, latency, throughput,
packet-rate, and resource-counter results. A slow path or a regression does not
block completion of the isolation system.

Measurements in the experiment and qualification documents are retained as
historical baselines. They are diagnostic information only. They do not define
required limits, required test durations, required hardware, mandatory
comparisons, or pass/fail thresholds. Missing or unfavorable performance data
must not make a functional or security qualification record fail.

Future work can improve performance after isolation is complete and working.
Such work can add benchmarks and targets for that optimization change, but it
must not retroactively turn the current isolation implementation into an
unqualified state.

Functional and security requirements remain mandatory. These include bounded
memory and queue ownership, protocol limits, cancellation, fairness, cleanup of
owned native state, rejection of foreign resources, fail-closed authorization,
and control progress without deadlock. Tests can apply finite timeouts to find
hangs. Those timeouts are correctness safeguards, not performance budgets.
