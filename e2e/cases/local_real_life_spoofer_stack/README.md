# Local real-life Spoofer stack

## Purpose

This case is a Docker version of the workstation graph in `local/alm.hujson`.
It tests the complete outer transport used by that graph:

```text
VTun transport
  `-- TunJoiner transport
       `-- Spoofer transport
            `-- OutNetwork direct
```

The existing `local_real_life_stack` case uses a managed Amnesia node as its
outer transport. This case uses Spoofer, as the local graph does. It keeps the
same nested Tailscale, DNS cache, SOCKS routing, TLS interception, DefaultTun,
process selection, and live rule-update layers.

The `local/alm.hujson` file is a design reference only. This case contains its
own configuration and does not read the gitignored `local/` directory during
its build or run.

Run it with:

```sh
./e2e/run.sh local_real_life_spoofer_stack
```

The case runner supplies the required Docker privileges. It does not use host
network mode. Routes, nftables state, TUN devices, and bpffs mounts stay in
temporary container namespaces.

## Docker topology

The runner creates four containers, one Docker bridge, and one temporary
volume:

1. A public HTTP origin reports its destination, remote source, and path.
2. An example HTTP origin supplies the destination used by filters.
3. A combined service container runs a TLS Headscale server, authoritative
   dnsmasq fixture, and a second Almagest daemon as the tailnet peer.
4. The main container runs the graph under test.

The origin and Headscale service scripts are shared with
`local_real_life_stack`. Standalone mode omits its Amnesia-only route and
source guard and adds the DNS fixture to the Headscale container.

All certificates, Headscale state, authentication keys, and WireGuard private
keys are generated for each run. The runner removes all containers, the
network, and the volume after success, failure, or interruption.

## Graph paths

The main graph uses a non-routable `192.0.2.2` address in `VTun transport`.
Spoofer consumes those packets and opens real host sockets through
`OutNetwork direct`. The HTTP origin therefore sees the main container's
Docker address. This verifies forwarding across the TUN boundary instead of
only checking that the nodes report a ready state.

```text
OutNetwork direct
  +-- HttpClient A
  +-- Proxy direct -> NetworkRouter direct
  `-- Spoofer transport <- TunJoiner transport
       +-- VTun transport
       |    +-- DnsClient transport -> DnsCache vt
       |    +-- HttpClient B
       |    +-- Proxy Spoofer only -> NetworkRouter Spoofer only
       |    +-- Tailscale control traffic
       |    `-- Tailscale WireGuard UDP traffic
       `-- default slot from TunSplitter transport

DefaultTun
  `-- TunSplitter lH
       +-- UDP port 53 -> Tun2Dns default -> DnsCache vt
       +-- selected destination or application -> DROP
       `-- VTun applications
            +-- ordinary destination -> Spoofer transport
            `-- 100.64.0.0/10 -> WireGuard Tailscale -> HttpServer SO
```

The main node and peer use Headscale's sequential addresses `100.64.0.2` and
`100.64.0.1`. DERP is disabled. The test requires a `direct-udp` peer path and
captures real nested WireGuard packets.

## Assertions

The test checks:

- all graph nodes become operational and stay stable;
- direct and Spoofer-backed HTTP clients reach the origin;
- DNS through VTun and Spoofer populates persistent cache storage;
- Tailscale control, MagicDNS, direct UDP, and tailnet HTTP work through the
  outer Spoofer transport;
- direct and Spoofer-backed SOCKS proxies enforce their destination rules;
- HTTP and HTTPS interception reach the internal HTTP server;
- DefaultTun sends normal traffic through the application graph;
- a p-mark-excluded process bypasses a destination that TunSplitter blocks;
- UDP DNS interception, destination rules, and application rules work;
- live rule removal and restoration work on both network routers and the TUN
  splitter;
- DNS cache state and all node states remain valid after live changes.

## Repetition

Run sequential repetitions with:

```sh
for run in $(seq 1 30); do
  E2E_JOBS=1 ./e2e/run.sh local_real_life_spoofer_stack || exit
done
```

Each run creates a new Docker network, addresses, certificates, Headscale
database, Tailscale identities, keys, and daemon storage.
