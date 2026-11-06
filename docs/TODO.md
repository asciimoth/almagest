# TODO

## Fixes and performance

- [ ] Fix operation after the host wakes from sleep
- [ ] Fix long node titles in the panel
- [ ] Fix races and overhead in compute (logic) nodes and links
  - Add scalar/control input and output ports to network nodes
- [ ] Make timeouts configurable, including HTTP client and SOCKS server
  handshake timeouts
- [ ] Review log levels and keep secret values out of logs
- [ ] Consider a different userspace TCP/IP stack, such as
  <https://github.com/soypat/lneto>

## Tests and development

- [ ] Add undo/redo tests for all existing topology tests
- [ ] Test every network output node with every network input node
  - Include pairs where each node is wrapped in SOCKS
- [ ] Generate an SBOM
- [ ] Rewrite Python and Bash development scripts and helpers in JavaScript or
  Go
- [ ] Add complex build tags to include or exclude nodes at compile time
  - Keep `lib/` useful to third-party projects that need only some nodes
- [x] Linux packages, source installer, and NixOS service
- [x] Windows MSI packaging and tests

## Documentation

- [ ] Guide for porting to new platforms
- [ ] Man pages
- [ ] How-to guides:
  - Install the almagestd CA in systems and applications
  - Configure SOCKS proxy use in applications and operating systems
  - Find a rule for filtering a specific application or process
  - Script and integrate tools with Almagest
    - Trigger nodes through the API
    - Call host commands
  - Pipe the admin API over SSH
- [x] Rich docs rendering with graph examples
- [x] Localized panel and node docs
- [x] Portable docs and browser demo

## Panel and desktop UX

- [ ] Add example placeholders to all node menu fields
- [ ] Add a log viewer to the panel
  - Search and filter logs
  - Show logs for a specific node
- [ ] Add rich visual process/application selection for filtering in DefaultTun
  and possibly LocalProxy
  - Show application logos where available
- [ ] Add a graphical geolocation selector for VPN clients and similar nodes
- [ ] Let protected config replace the panel's static content for customization
- [x] Desktop admin client with docs and tray

## Configuration and runtime

- [ ] Add config templates with an immutable template and mutable config
- [ ] Omit empty/default values in nodes' private fields
- [ ] Support live almagestd reload on SIGHUP
- [ ] Allow configuration of IP address allocation
- [ ] Fetch host environment data, such as country and local network properties
  - Allow graph flows to switch according to the current location
- [x] Hujson config editing in the admin panel
- [x] Config snapshots with snapshot/rollback in the admin panel
- [x] Snapshot import/export and packaged examples

## Graph and network nodes

- [ ] Add reverse port mapping, with cycle checks
- [ ] Add port mapping
- [ ] Add TCP/UDP/HTTP(S) reverse proxy nodes
- [ ] Add proxy chaining, including a SOCKS chain node
  - Outline SDK includes a SOCKS5 stream dialer that can help with the chain
    implementation
  - Accept a list of nested SOCKS client configurations as an object input
- [ ] Add a bad-network node for jitter and delays
- [ ] Add a dispatcher node for round-robin and random selection
- [ ] Add rate-limit nodes for TUN and Network links
  - Cover outgoing dial and incoming listen/accept connections
- [ ] Add UDP over TCP
  - <https://github.com/mullvad/udp-over-tcp>
  - <https://github.com/jonhoo/udp-over-tcp>
  - <https://sing-box.sagernet.org/configuration/shared/udp-over-tcp/>
- [ ] Add a camouflage traffic generator
  - Record and replay custom traffic patterns
- [ ] Add router mode with helpers to route all router traffic through Almagest
- [ ] Investigate <https://github.com/norouter/norouter>
- [ ] Add shell callbacks for IP/subnet filtering
- [ ] Add UPnP support?
- [ ] Add a multiplexer
  - <https://github.com/xtaci/smux>
  - <https://github.com/hashicorp/yamux>
- [x] Graph engine, typed links, and worker tracking
- [x] Network and TUN routing and firewall nodes
- [x] HTTP, mock network, and diagnostic nodes

## Filtering and block lists

- [ ] Create a compact, low-memory database/file format for large FQDN and IP
  filter or block lists
  - Support filters in any node, including ad blocking
  - Support automatic list updates
  - Consider a custom list format ("свой йоба формат со списками фильтров")
- [ ] Add a dedicated adblock node for DNS, TUN, and Network links
  - Existing nodes can implement ad blocking, but a dedicated node would help
  - [ ] Fetch and update built-in block lists

## DNS

- [ ] Add DoH, ODoH, and DoQ
  - Outline SDK includes a DoH resolver; ODoH and DoQ still need other
    implementations
- [ ] Add mDNS?
- [ ] Add a DNS hijack node for TUN and Network links
  - Existing nodes can implement this, but a dedicated node would help
- [x] meshname
- [x] DoT
- [x] DNS graph nodes and cache management

## TLS and DPI

- [ ] Manage server-side TLS configs
- [ ] Support custom network sniffer patterns as well as built-in patterns
- [ ] Add a reverse sniffer
- [ ] Add UDP protocol support to DPI tools
- [ ] Add more protocol classifiers
- [ ] Investigate Zapret, GoodbyeDPI, and similar DPI bypass tools
  - Outline SDK includes TLS ClientHello fragmentation and stream splitting for
    focused bypass nodes
  - <https://github.com/ValdikSS/GoodbyeDPI>
  - <https://github.com/SadeghHayeri/GreenTunnel>
  - <https://github.com/dilluti0n/dpibreak>
  - <https://github.com/xvzc/spoofdpi>
- [x] Sniffer
- [x] Warnings for hazardous certificates
- [x] Client TLS trust and revocation management
- [x] Manually add Russian government certificates to the hazardous certificates
  list
  - Manual sources have higher priority than parsed sources

## Secrets and admin API

- [ ] Add admin API authentication
  - Consider passwords, access tokens, and FIDO
  - Support narrow token/key scopes, including TUN node control and secrets
    access
  - Configure admin API access keys through protected config, possibly using
    environment variables
- [ ] Add an MCP API
- [x] Admin API, graph panel, and local CLI transport
- [x] Database-backed secrets storage
- [x] Use secrets for WireGuard private keys
- [x] Use secrets for optional SOCKS client, SOCKS server, and local proxy
  passwords

## CLI and access

- [ ] Produce machine-readable JSON by default when output is not a TTY
- [ ] Add a semi-interactive TUI mode with fzf-like pickers when output is a TTY
- [ ] Add a CLI subcommand to add a user to the daemon access group
  - Support sudo or doas
- [ ] Add a desktop flow to add a user to the daemon access group
  - Show a popup to ask the user
  - Implement the privileged action fully in the Rust backend; the JavaScript
    frontend only asks and sends the response
- [x] CLI graph, DNS, TLS, secrets, and snapshot commands

## System network and integrations

- [ ] Drop resolved DNS caches after their configuration changes
- [ ] Add a rule to allow direct pings
- [ ] Add more pre-filter and routing rule options
  - Destination address
  - Flatpak application?
  - Docker container?
  - VM?
- [ ] Allow emulation of all sysnet features through the debug admin API
- [ ] Integrate with Docker and VMs for per-container or per-VM routing
- [ ] Integrate with Qubes OS for per-domain routing
- [ ] Add Shelter integration on Android for profile-based routing
- [ ] Integrate with distributed firewalls
  - CrowdSec
  - diswall
- [ ] Investigate upstream integration with formular, configger, and pasta
- [x] Sysnet Linux
- [x] Sysnet Windows
- [x] Windows daemon and named-pipe admin transport

## Runtime isolation

- [ ] Add sandboxed drivers
  - OpenVPN client?
  - alfis?
  - mycelium network?
- [ ] Consider bubblewrap as an automatic isolation/sandbox fallback
- [ ] Complete isolation on Windows and ARM Linux
- [ ] Complete strict sandbox mode on non-amd64 Linux targets
- [ ] Add performant IPC for isolated processes
- [ ] Obtain a third-party isolation audit
- [ ] Enable isolation by default when ready
- [x] Linux daemon process isolation
- [x] Cross-platform subprogram sandboxing

## WireGuard and related services

- [ ] Import standard WireGuard config format
- [ ] Investigate NAT traversal:
  - <https://github.com/alex14fr/wgsig>
  - <https://github.com/malcolmseyd/natpunch-go>
- [ ] Investigate <https://github.com/ClusterM/wg-obfuscator>
- [ ] Investigate NordVPN's NordLynx protocol
- [ ] Add TailscaleCtrl peer approval
- [x] AmnesiaWG extension
- [x] WireGuard nodes and TUN route propagation
- [x] Tailscale client
- [x] Amnesia client

## Proxy, VPN, and overlay protocols

- [ ] Add SSH network proxy support
  - Investigate sshuttle-like routing
  - Port forwarding
  - Shell pipe
- [x] Add Shadowsocks client and server nodes (classic AEAD TCP and UDP, via
  Outline SDK)
- [x] Add Yggdrasil network support
- [ ] Add VLESS or other protocols used by Amnesia
  - Investigate extra features such as per-device IDs
- [ ] Add Reticulum network support?
- [ ] Add PPTP
- [ ] Add Nebula
  - <https://github.com/slackhq/nebula>
  - <https://github.com/forgekeep/nebula-mesh>
- [ ] Add Tink?
- [ ] Add NaiveProxy
- [ ] Add Stunnel / [go-tunnel](https://github.com/opencoff/go-tunnel)
- [ ] Add iodine?
- [ ] Add Cloak?
- [ ] Investigate <https://github.com/jpillora/chisel>
- [ ] Investigate Latern
- [ ] Add a DNS port for the Tor resolve extension in the SOCKS client
- [x] SOCKS client with a noproxy string field
- [x] SOCKS server
- [x] Local proxy with filtering and secret-backed passwords

## Reverse proxy services

- [ ] Add ngrok-like reverse proxy service clients
  - ngrok
  - AWS Tunnel reverse proxy?
  - <https://github.com/andydunstall/piko>
- [ ] Add a NetBird client?

## Tor integration

- [ ] Investigate <https://github.com/cretz/bine>
- [x] Sandboxed Tor driver and onion services

## Monitoring and statistics

- [ ] Show real-time DNS requests in a dedicated admin panel tab
- [ ] Show Network/TUN traffic flows
- [ ] Show CPU, RAM, and other resource use?

## Protocol research

- [ ] Review features and supported protocols for possible Almagest support:
  - <https://gost.run/en/>
  - sing-box
  - tun2socks
  - xray / v2ray
  - <https://github.com/UniformlyR4ndom/tlstap>
  - nc / sockat

## Plugin system?

- [ ] Compare Go plugins, IPC, lua, and WASM
