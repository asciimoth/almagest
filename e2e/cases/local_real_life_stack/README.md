# Local real-life networking stack

## Purpose

This case is a Docker-only version of the workstation graph in
`local/alm.hujson`. It tests the complete graph as one system. It does not use
the host Tailscale service, the public Tailscale control plane, an external
Amnesia service, public DNS, or a public HTTP server.

The `local/alm.hujson` path is a design reference only. The case contains its
own graph and protected configuration. It does not read or copy any file from
the gitignored `local/` directory during image build, setup, or execution.

Run it with:

```sh
./e2e/run.sh local_real_life_stack
```

The case-specific runner supplies the required Docker privileges. It does not
use host network mode. Routes, nftables rules, TUN devices, DNS changes, and
bpffs mounts stay in container namespaces. The runner removes all containers,
networks, and the shared volume after success, failure, or interruption.

The case image includes `tcpdump`. Packet traces stay in the temporary
containers and are printed only when the case fails.

## Docker topology

The runner creates five containers, two temporary Docker bridges, and one
temporary volume.

1. **Public origin**

   - Connects only to the simulated Internet bridge.
   - Runs an HTTP server on port 80.
   - Returns its destination identity, remote source address, method, and path.
     These values prove whether traffic used direct or Amnesia egress.

2. **Example origin**

   - Connects only to the simulated Internet bridge.
   - Runs the same HTTP service under a different dynamic Docker address.
   - Supplies the real destination for both the `/etc/hosts` and DNS domain
     variants.

3. **Amnesia self-host service**

   - Connects to the control bridge and the simulated Internet bridge.
   - Runs a real `amneziawg-go` server on UDP port 51861.
   - Runs a local HTTPS Amnesia API-v2 gateway on port 18490. Its private HTTP
     backend listens only on loopback port 18091.
   - Runs dnsmasq on port 53. It serves `public.stack.test`,
     `intercept-dns.stack.test`, and the Headscale control name.
   - Routes `10.205.10.1`, the managed client address, through the Amnesia TUN.
   - Masquerades only traffic that exits to the simulated Internet bridge.
     Traffic between the Amnesia client and colocated control services keeps
     its inner VPN address.
   - Publishes the activation key, gateway public key, and local CA through the
     temporary volume. It installs the managed client public key after the main
     daemon creates it.

4. **Headscale service and tailnet peer**

   - Is a separate container and process, but shares the Amnesia container's
     Docker network namespace. This places the self-hosted control service and
     tailnet peer behind the VPN gateway without host routes.
   - Runs a TLS Headscale server with a new SQLite database, an embedded local
     DERP map, and an allow-all ACL. Direct peer traffic has DERP disabled.
   - Creates a reusable pre-authentication key.
   - Runs a second `almagestd` as `peer.stack.test` with `TailscaleCtrl`,
     `WireGuard`, `VTun`, the Headscale-derived `NetworkFirewall`, and
     `HttpServer SO`.
   - Generates a new peer WireGuard private key inside the temporary container
     for each run.
   - Receives tailnet address `100.64.0.1` from the new sequential Headscale
     address pool. The main node then receives `100.64.0.2`.
   - Uses an nftables input guard on the Headscale HTTPS port. It accepts local
     peer traffic and traffic with source `10.205.10.1`, but drops a direct
     request from the main container's Docker address.

5. **Main Almagest container**

   - Connects to both Docker bridges and runs the graph under test.
   - Runs the managed `Amnesia` node against the local service.
   - Runs Tailscale control and direct WireGuard peer traffic through the
     Amnesia VTun.
   - Runs `DefaultTun`, UDP DNS interception, process and destination filters,
     two SOCKS proxies, HTTP and TLS interception, three DNS query nodes, and
     three HTTP clients.
   - Uses persistent daemon file storage inside the container. The test reads
     DNS cache keys through the daemon storage API.
   - Generates a new main Tailscale WireGuard private key during setup.

All TLS private keys, Amnesia keys, WireGuard keys, Headscale keys, activation
data, and authentication keys are generated inside the temporary containers.
The checked-in case contains no private key. The runner deletes the containers
and their shared volume on exit.

## Initial network rules

The main container has a normal direct Docker path to the public origin before
`almagestd` starts. It does not have `10.205.10.1`, `10.205.20.1`, or a
tailnet address. The Headscale input guard rejects the main container's direct
Docker source.

The setup removes the main container's connected route for the simulated
Internet subnet, retains a host route to its Docker gateway, and installs that
gateway as the default route. This makes ordinary same-container traffic
eligible for `DefaultTun`. Marked application bypass traffic still uses the
original Docker gateway.

Before daemon startup, the test requires these results:

- The public origin is reachable directly.
- Headscale HTTPS is not reachable directly.
- The Amnesia gateway TUN address `10.205.20.1` is not reachable.
- `HttpServer SO` at `100.64.0.1:18080` is not reachable.

After startup, the principal graph paths are:

```text
OutNetwork direct
  +-- HttpClient A
  +-- Proxy direct -> NetworkRouter direct
  `-- Amnesia VPN -> VTun Amnesia
       +-- DnsClient VPN -> DnsCache vt
       +-- HttpClient B
       +-- Proxy Amnesia only -> NetworkRouter Amnesia only
       +-- Tailscale control traffic
       `-- WireGuard Tailscale direct UDP traffic

DefaultTun
  `-- TunSplitter lH
       +-- UDP port 53 -> Tun2Dns default -> DnsCache vt
       +-- selected destination or application -> DROP
       `-- application VTun -> outer splitter
            +-- normal destination -> Amnesia VPN
            `-- 100.64.0.0/10 -> WireGuard Tailscale -> HttpServer SO
```

The full traffic matrix uses `AutoTunSplitter VPN`. It learns the tailnet
route from the WireGuard TUN configuration. The test then restarts the same
graph with the original `TunSplitter VPN` and its manual subnet rules. A
focused compatibility pass checks the normal and tailnet slots again.

The origin reports the main Internet-bridge address for direct traffic. It
reports the Amnesia service's Internet-bridge address for VPN traffic because
the gateway applies source NAT at that boundary.

## Domain and application variants

The case uses two names for the example origin:

- `intercept-hosts.stack.test` exists only in the main container's
  `/etc/hosts`.
- `intercept-dns.stack.test` is served by dnsmasq through Amnesia and is stored
  in `DnsCache vt`.

Remote SOCKS name resolution does not read the main container's `/etc/hosts`.
Therefore, both names test the enabled direct-router drop rule before
resolution, but the SOCKS interception and router toggle checks use the
DNS-served name. `TunSplitter lH`, which handles main-container traffic, tests
both names.

`direct-fetch` replaces Firefox. Its `comm` identity is excluded from
`DefaultTun`, so it keeps direct Internet access. The helper starts before it
opens the checked socket to model a long-lived browser process.

`blocked-fetch` replaces wget. `TunSplitter lH` drops it with both `cmd` and
`comm` rules.

## Operational readiness

Each service publishes a readiness file only after its own active checks pass.

The Amnesia container checks:

- the `amneziawg-go` UAPI socket and TUN interface;
- the private API backend and public TLS health endpoint;
- the dnsmasq A record and positive TTL;
- both origin services through the simulated Internet bridge.

The Headscale container checks:

- the Headscale TLS health endpoint;
- user and reusable auth-key creation;
- `Tailscale Peer` and `WireGuard Peer` labels are `up`;
- the ACL-derived firewall reports four effective rules;
- `HttpServer SO` is listening before the input guard is enabled.

The main test then:

1. Waits for the managed Amnesia node to become `up`.
2. Gives its generated public key to the local Amnesia service and waits for
   peer installation.
3. Checks the operational label of every graph node.
4. Checks both SOCKS listeners in the daemon log.
5. Requires the full graph state to stay stable for five seconds.
6. Requires a Tailscale `direct-udp` path whose local endpoint is inside the
   outer Amnesia tunnel.
7. Sends a real `HttpClient Tailnet` request before it starts the test matrix.

The final graph-state check repeats after all live configuration changes.

## Traffic checks

### DNS and cache storage

- `DnsQuery public` resolves `public.stack.test` with `NOERROR`.
- `DnsQuery intercepted` resolves `intercept-dns.stack.test` with `NOERROR`.
- `DnsQuery tailnet` resolves `peer.stack.test` with `NOERROR` through
  Tailscale MagicDNS.
- Cache keys for the public and intercepted queries are visible in storage
  `default`.
- A UDP DNS request sent to unrelated address `198.51.100.53:53` is captured
  by `DefaultTun`, sent through `Tun2Dns default`, and returns the dnsmasq
  answer with `NOERROR`.
- The cached intercepted-domain key is still present after all traffic and
  filter changes.

### HTTP clients and nested VPNs

- `HttpClient A` reaches the public origin directly.
- `HttpClient B` reaches it through Amnesia without extra destination or
  application limits.
- `HttpClient Tailnet` reaches `HttpServer SO` through Tailscale while both
  Headscale control traffic and direct peer UDP use the outer Amnesia tunnel.
- Tailscale reports `direct-udp`. In this case, direct means direct between the
  two tailnet nodes inside the outer Amnesia network, not DERP.

### SOCKS routers and interception

- `Proxy direct` reaches the public origin directly.
- With filtering enabled, `NetworkRouter direct` blocks both target names when
  curl uses `--socks5-hostname`.
- `Proxy Amnesia only` reaches the public origin through Amnesia.
- HTTP for `intercept-dns.stack.test` is redirected to
  `HttpServer intercepted`.
- HTTPS for that name is intercepted by `TlsMitm`, remapped by
  `TlsTerminator`, and redirected to the same HTTP server. Curl trusts the
  built-in Almagest root CA obtained through the admin API.
- HTTP and HTTPS also work with `--socks5`, where curl resolves the name first.
  The router uses reverse entries in `DnsCache vt` to recover the domain from
  the destination address.

### DefaultTun and process routing

- Ordinary curl traffic enters `DefaultTun` and exits through Amnesia.
- The long-lived `direct-fetch` process is excluded and reaches the origin
  directly.
- The excluded process cannot reach Headscale directly, which confirms that
  the successful Tailscale controller path used Amnesia.
- Both the `/etc/hosts` target and DNS/cache target are blocked.
- `blocked-fetch` is blocked by application identity.
- Ordinary main-container traffic reaches both the simulated Internet and
  `HttpServer SO` in the tailnet with the automatic splitter.
- After a restart, the same two paths work with the original manual splitter
  rules.

## Live filter matrix

The test changes rules through each node's live menu, checks the open state,
restores the original rules, and checks the filtered state again.

| Node | Filters disabled | Filters restored |
|---|---|---|
| `NetworkRouter direct` | The DNS-served target reaches the example origin directly. | The same target is blocked. |
| `NetworkRouter Amnesia only` | The DNS-served target reaches the example origin through Amnesia. | The same target is redirected to the internal HTTP server. |
| `TunSplitter lH` | Both domain variants and `blocked-fetch` reach the example or public origin through Amnesia. UDP DNS interception remains active. | Both destination variants and `blocked-fetch` are blocked again. |

## Repetition

Run sequential repetitions with:

```sh
for run in $(seq 1 30); do
  E2E_JOBS=1 ./e2e/run.sh local_real_life_stack || exit
done
```

Every repetition creates new Docker networks, addresses, certificates,
Headscale database, Tailscale identities, Amnesia keys, and daemon storage.
