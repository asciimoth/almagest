# Almagest

<img alt="Almagest" src="./logo.svg" height="200" width="200" style="border-radius:50%;"/>
<svg viewBox="-101 -101 201 201" xmlns="http://www.w3.org/2000/svg">
  <circle r="100" x="0" y="0" fill="#1e1e2e" />

<circle r="90" cx="0" cy="0" fill="#1e1e2e" stroke="#45475a" stroke-width="1" />
  <circle r="75" cx="0" cy="0" fill="#1e1e2e" stroke="#45475a"
    stroke-width="1" />
  <circle r="63" cx="0" cy="0" fill="#1e1e2e" stroke="#45475a"
    stroke-width="1" />
  <circle r="47" cx="0" cy="0" fill="#1e1e2e" stroke="#45475a"
    stroke-width="1" />
  <circle r="30" cx="0" cy="0" fill="#1e1e2e" stroke="#45475a"
    stroke-width="1" />
  <circle r="20" cx="0" cy="0" fill="#1e1e2e" stroke="#45475a"
    stroke-width="1" />
  <circle r="12" cx="0" cy="0" fill="#1e1e2e" stroke="#45475a"
    stroke-width="1" />

<circle r="6" cx="0" cy="0" fill="#a6e3a1" class="planet" />
  <circle r="2" cx="0" cy="12" fill="#cdd6f4" class="planet">
    <animateTransform
      attributeName="transform"
      begin="0s"
      dur="4s"
      type="rotate"
      from="0 0 0"
      to="360 0 0"
      repeatCount="indefinite" />
  </circle>
  <circle r="3" cx="20" cy="0" fill="#9399b2" class="planet">
    <animateTransform
      attributeName="transform"
      begin="0s"
      dur="5s"
      type="rotate"
      from="0 0 0"
      to="360 0 0"
      repeatCount="indefinite" />
  </circle>
  <circle r="4" cx="0" cy="-30" fill="#f2cdcd" class="planet">
    <animateTransform
      attributeName="transform"
      begin="0s"
      dur="6s"
      type="rotate"
      from="0 0 0"
      to="360 0 0"
      repeatCount="indefinite" />
  </circle>
  <circle r="10" cx="-47" cy="0" fill="#f9e2af" class="planet">
    <animateTransform
      attributeName="transform"
      begin="0s"
      dur="7s"
      type="rotate"
      from="0 0 0"
      to="360 0 0"
      repeatCount="indefinite" />
  </circle>
  <circle r="4" cx="0" cy="63" fill="#f38ba8" class="planet">
    <animateTransform
      attributeName="transform"
      begin="0s"
      dur="8s"
      type="rotate"
      from="0 0 0"
      to="360 0 0"
      repeatCount="indefinite" />
  </circle>
  <circle r="6" cx="75" cy="0" fill="#fab387" class="planet">
    <animateTransform
      attributeName="transform"
      begin="0s"
      dur="9s"
      type="rotate"
      from="0 0 0"
      to="360 0 0"
      repeatCount="indefinite" />
  </circle>
  <circle r="6" cx="0" cy="-90" fill="#eba0ac" class="planet">
    <animateTransform
      attributeName="transform"
      begin="0s"
      dur="10s"
      type="rotate"
      from="0 0 0"
      to="360 0 0"
      repeatCount="indefinite" />
  </circle>
</svg>

Almagest is a network toolkit with a visual graph editor. It combines VPNs,
proxies, DNS services, routing, and traffic filters in one local service.
Connect nodes to define where traffic goes and how each step processes it.

Build a local proxy for one application, route host traffic through several
networks, or combine tunnels and proxies in one path.

> [!CAUTION]
> Almagest is pre-alpha software. Features and configuration can change. Do not
> use it in production.

## Features

- [VPN tunnels](https://asciimoth.github.io/almagest/docs/?page=node%2Falmagest%2FWireGuard):
  connect WireGuard and AmneziaWG peers. Use tunnels for selected applications
  or host traffic.
- [SOCKS proxies](https://asciimoth.github.io/almagest/docs/?page=node%2Falmagest%2FSocksClient):
  use SOCKS4, SOCKS4a, and SOCKS5 clients and servers. Give local applications a
  proxy into any connected network. Client transports include TLS and WebSocket;
  SOCKS5 supports TCP and UDP.
- [Routing and filtering](https://asciimoth.github.io/almagest/docs/?page=almagest%2Fprocess-filtering):
  split traffic by address, domain, port, or protocol. Match applications,
  processes, users, and groups where the platform supports them. Apply firewalls
  to packets or network operations, and remap addresses and ports.
- [DNS](https://asciimoth.github.io/almagest/docs/?page=almagest%2Fdns): combine
  static records, persistent caches, fallback resolvers, and routing rules. Send
  queries through selected networks over UDP, TCP, or TLS. Serve graph DNS to
  other clients or use it for captured host traffic.
- [Tunnel and proxy chains](https://asciimoth.github.io/almagest/docs/?page=almagest%2Ftypes):
  put a VPN through a proxy, a proxy through a VPN, or one WireGuard tunnel
  inside another. Convert between IP packets and socket operations to connect
  these layers. Each transport must support the operations required by the next
  layer.
- [Protocol inspection](https://asciimoth.github.io/almagest/docs/?page=node%2Falmagest%2FSniffer)
  and [TLS](https://asciimoth.github.io/almagest/docs/?page=almagest%2Ftls):
  route or filter TCP connections by protocol, HTTP request data, TLS SNI, or
  ALPN. Add or terminate TLS, or intercept connections for inspection. Manage
  certificates, pin keys, and limit a certificate authority's trust to selected
  domains or IP ranges.
- [Graph logic](https://asciimoth.github.io/almagest/docs/?page=almagest%2Flogic):
  combine values, variables, conditions, loops, and timed triggers. Switch
  traffic paths while the service runs. Use virtual networks and built-in HTTP
  clients and servers to test a graph. Graph logic is still work in progress.
- [Tor](https://asciimoth.github.io/almagest/docs/?page=node%2Falmagest%2FTorDriver):
  send TCP traffic through Tor, use separate circuit isolation identities, and
  publish onion services. Tor runs as a separate program under
  administrator-defined policy.
- Service clients: import Amnezia VPN service keys and self-hosted profiles with
  [Amnesia](https://asciimoth.github.io/almagest/docs/?page=node%2Falmagest%2FAmnesia).
  Connect to Tailscale or Headscale with
  [TailscaleCtrl](https://asciimoth.github.io/almagest/docs/?page=node%2Falmagest%2FTailscaleCtrl),
  including MagicDNS and firewall policy outputs.

## How it works

Nodes connect through typed ports. Almagest includes a userspace TCP/IP stack
that converts between packets and socket operations. This lets a graph connect
VPN tunnels, proxies, filters, and services at different network layers.

Each path reaches host applications or external networks through explicit
boundary nodes. Several independent paths can run in one graph, including
virtual networks for tests.

The `almagestd` service keeps the graph running when you close the editor. The
desktop app, web panel, and `almagest-cli` manage the graph, logs, secrets, DNS
caches, and TLS policies. Configuration uses HuJSON (JSON with comments and
trailing commas). Snapshots save the editable configuration and persistent data;
protected host policy stays separate.

## Platform support

Linux packages cover amd64 and arm64. The current Windows package targets
Windows Server 2022 on amd64. Host routing, process matching, and sandbox
features differ by platform. See the
[Linux package notes](./packaging/linux/README.md) and
[Windows support notes](./packaging/windows/README.md).

See
[isolation and sandboxing](https://asciimoth.github.io/almagest/docs/?page=almagest%2Fsecurity)
for the available process restrictions and their limits. Linux has experimental
integration with a
[separate kill-switch service](https://asciimoth.github.io/almagest/docs/?page=almagest%2Fkillswitch).

## Install from source

On a Debian-, RPM-, or Arch-based Linux system, install
[`just`](https://github.com/casey/just), clone this repository, and run:

```sh
just install daemon app
```

Specify `daemon`, `app`, or both. The `daemon` part includes the command-line
client. The command checks build and runtime requirements and prints package
installation commands when available. It preserves existing files in
`/etc/almagest`.

The command does not enable the service. After installation, start it with:

```sh
sudo systemctl enable --now almagestd.service
```

Open the desktop app and load **Hello Network** or **Mock Network** from
**Snapshots → Examples**. These examples do not change host routing. Loading an
example replaces the current editable configuration and data, so save a snapshot
first if you already have a setup.

## Documentation and development

The app and web panel include guides, node references, and working examples.
Start with
[Getting started](https://asciimoth.github.io/almagest/docs/?page=almagest%2Fstart),
[Nodes, ports, and links](https://asciimoth.github.io/almagest/docs/?page=almagest%2Fgraph),
or
[Service and command line](https://asciimoth.github.io/almagest/docs/?page=almagest%2Fdaemon-cli).

The engine and nodes are also available as a reusable Go library in `lib/`. See
[ARCHITECTURE.md](./ARCHITECTURE.md) for the code layout, build commands, and
tests.

## License

Almagest is licensed under the
[GNU General Public License v3.0 or later](./LICENSE).
