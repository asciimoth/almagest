# Experimental nodes

This package contains node classes that do not fit the complete Almagest
abstraction design. Each class name starts with `X`. The library does not add
these classes to its default class list. A host must create each class and pass
it to `BuildEngine` as an extra class.

Experimental nodes can use host resources or have platform requirements that
normal library nodes do not have. Treat their graph configuration as untrusted
runtime input, but keep executable paths, identities, and similar host policy in
protected daemon configuration.

## XTorDriver

`almagest/XTorDriver` owns one foreground Tor client process through
`github.com/asciimoth/tor-driver`. It has one right-directed `out` Network input
and creates left-directed Network outputs from its structured node menu:

- `Network NAME` is an independent Tor client Network. It supports session or
  per-connection circuit isolation and optional destination address and port
  isolation.
- `Onion NAME` is a listen-only Network for one v3 onion service. Its reserved
  virtual ports, stream policy, persistent identity, and authorized client
  public keys are part of the node configuration.

The node also supports all daemon-wide typed tor-driver options, plain and
obfs4 bridges, and client-side v3 authorization. It does not expose raw torrc
text, arbitrary process arguments, or executable paths.

Private v3 service keys and client authorization private keys use Almagest
node-scoped secrets. An onion service with an empty `keySecret` creates a new
secret during its first successful start. A client authorization with no
`privateKeySecret` does the same. The saved graph stores only the returned
secret selectors.

Tor logs are always forwarded through the node logger. The central almagestd
logger controls filtering and output.

The protected daemon section controls the host boundary:

```hujson
{
  "Experimental": {
    "TorDriver": {
      // Empty paths use tor-driver PATH discovery.
      "TorExecutable": "/usr/bin/tor",
      "Obfs4Executable": "/usr/bin/obfs4proxy",
      "TempRoot": "/run/almagest/tor",
      "StateRoot": "/var/lib/almagest/tor",
      "Sandbox": "dynamic",
      "StartupTimeout": "30s",
      "CommandTimeout": "10s",
      "DialTimeout": "90s",
      "ShutdownTimeout": "10s",
      "MaxCircuitDirtiness": "10m",
      "CircuitBuildTimeout": "45s",
      "Identity": {"UID": 65534, "GID": 65534},
    },
  },
}
```

On Linux, a root almagestd process with no explicit identity asks tor-driver to
select the host `nobody` account. Process and containment decisions are written
to the node log. They are not part of the normal node status. The experimental
node never routes Tor relay sockets through a native fallback: Tor's external
connections use only the Network attached to `out`.

Runtime work stays outside Pasta callbacks. Full rebuilds, readiness waits,
outbound replacements, event subscriptions, and shutdowns carry runtime or
configuration generations. A stale completion cannot publish outputs or mark a
replacement process ready. Disconnecting `out` withdraws published Networks;
reconnecting it uses tor-driver's live `SetOutbound` path and does not restart
the daemon.
