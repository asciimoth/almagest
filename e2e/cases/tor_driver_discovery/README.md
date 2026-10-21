# tor_driver_discovery

This Linux case exposes the deterministic Tor fixture as `tor` in `PATH` and
starts `almagest/TorDriver` without explicit sandbox program, template, storage
root, or launcher definitions. The E2E harness supplies its delegated cgroup
root so direct strict mode can qualify the full boundary. The case checks
protected-config and command-line discovery in sandbox mode `off`, `auto`, and
`strict`, including a SOCKS request through the node. It starts TorDriver with
an output consumer but no input network, checks that the node keeps waiting
without latching an error, and attaches the input while the daemon is running.
It then checks these runtime lifecycle operations:

- remove and restore an output consumer without restarting Tor;
- remove, replace, and rapidly churn the outbound input without restarting Tor;
- restart Tor through its menu control and verify that the child changes;
- delete TorDriver and verify that its child stops; and
- create and link a new TorDriver through the admin API.

Each operation includes a data-plane request after convergence. The full
sequence runs for all nine daemon-isolation and sandbox-mode combinations.

The image does not provide lyrebird or obfs4proxy. The case also verifies that
this optional discovery failure is reported but does not stop Tor traffic. In
direct daemon mode, it then removes the Tor executable and verifies that the
daemon and an unrelated node continue while TorDriver shows the discovery error
in its status interface.
