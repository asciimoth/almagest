# Tor nodes

`almagest/TorDriver` owns one Tor client through `tor-driver`. It has one
right-directed `out` Network input. Its menu creates isolated Tor client
Networks and listen-only v3 onion-service Networks.

The node has no direct process or filesystem access. It requests the owner
slot `tor`, template `tor`, and program `tor` from Almagest's `gobox` sandbox
service. Obfs4 uses the additional program ID `obfs4`. Protected sandbox policy
maps these logical IDs to executable images, child identity, storage, resource
limits, and isolation mode. The node cannot fall back to host execution.

The daemon can create this policy at startup from programs on its host `PATH`.
Set protected `Sandbox.DiscoverSystemPrograms` to `true`, or pass
`--discover-system-programs`. Discovery is off by default. It resolves `tor`
and, when present, `lyrebird` or `obfs4proxy`. Explicit protected program and
template entries take precedence over generated entries.

Automatic discovery is best effort. A missing executable, unreadable ELF
record, or missing runtime library does not stop the daemon and does not affect
unrelated nodes or explicit sandbox templates. The daemon records the failure
on the affected generated capability. `TorDriver` shows that error when it
tries to use the capability. Tor and each optional transport are inspected
separately: an unusable `lyrebird` or `obfs4proxy` disables only `obfs4`, so Tor
continues to work unless the node configuration requests that transport.

On Linux, discovery reads the executable's ELF metadata without running it.
The generated profile includes the dynamic loader, the complete linked-library
closure, and available Tor `geoip` data as exact read-only mappings. Absolute
RUNPATH entries are retained, so content-addressed layouts such as `/nix/store`
work in the private filesystem. The profile does not expose a complete host
library directory. On Windows, the native profile starts the discovered image
with the qualified write-restricted Low-integrity token and Job Object
boundary. Reads use the filtered daemon identity, which lets Tor load its
sibling DLLs and initialize Windows CryptoAPI. Filesystem writes also require
the stable instance SID and are limited to the protected temporary and
persistent roots.

The generated template supports sandbox mode `off`, `auto`, and `strict`.
`off` selects direct execution. On qualified Linux hosts, `auto` and `strict`
select the full Linux boundary. On Windows they select the documented native
Windows profile. Windows `strict` requires every mandatory native protection;
it fails before launch if one cannot be applied. Windows `auto` applies all
available compatible protections and continues when an optional protection is
unavailable. Both modes report the native profile's filesystem-view and private
network OS exclusions.

A minimal protected program definition uses the two required path inputs from
tor-driver:

```hujson
"Programs": {
  "tor": {
    "Executable": "/usr/bin/tor",
    "Builder": "fixed-v1",
    "Inputs": {
      "defaults-torrc": {"Kind": "path", "Required": true},
      "torrc": {"Kind": "path", "Required": true},
    },
    "Arguments": [
      {"Literal": "--defaults-torrc"}, {"Input": "defaults-torrc"},
      {"Literal": "-f"}, {"Input": "torrc"},
    ],
  },
},
"Templates": {
  "tor": {
    "AllowedClasses": ["almagest/TorDriver"],
    "Programs": ["tor"],
    "RuntimeProfile": "linux-landlock-v1",
    "Linux": {"UID": 65534, "GID": 65534},
  },
},
```

Use the platform-qualified runtime profile and limits that match the daemon
deployment. Add `obfs4` to the template and to the `tor` program descendants
when managed transports are enabled.

Private service keys and client authorization keys use node-scoped Almagest
secrets. Generated selectors are saved only after durable node configuration
persistence succeeds.

Runtime rebuilds, readiness, outbound replacement, events, and shutdown use
generation checks. A stale completion cannot publish outputs or mark a newer
runtime ready. Detaching `out` blocks Tor relay traffic. Reattaching it uses
tor-driver's live outbound replacement and does not restart Tor.
