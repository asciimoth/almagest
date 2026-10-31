Fixes

- [ ] Find out why there is so much workers respawn in daemon logs
  - Why there is so much dns query workers
- [ ] Fix work after host sleep wakeup

Optimisation

- [x] Compress builtin admin pamel
- [ ] Switch to other userspace TCP/IP stack implementation
  - <https://github.com/soypat/lneto> ?

Dev

- [ ] Better web dev setup (linters, etc)
- [ ] SBOM
- [x] Linux packages, source installer, and NixOS service
- [x] Windows MSI packaging and tests

Docs

- [ ] Guide for porting to new platforms
- [x] Rich docs rendering with graph examples
- [x] Localized panel and node docs
- [x] Portable docs and browser demo

Tests

- [ ] Add undo/redo tests analog for all existed topology tests

Graph and network

- [x] Graph engine, typed links, and worker tracking
- [x] Network and TUN routing and firewall nodes
- [x] HTTP, mock network, and diagnostic nodes

UX/QoL

- [ ] Make sure there are example placeholders in all node menus fields
- [ ] Dedicated admin panel tab for monitoring of all DNS requests in graph
- [ ] Review log levels
- [x] Desktop admin client with docs and tray

Configuration

- [ ] Config templates system for setups with immutable template and mutable
  config
- [x] Hujson config editing in admin panel
- [x] Config snapsots system with snapshot/rollback options in admin panel
- [x] Snapshot import/export and packaged examples
- [ ] Omitempty/omitdefault in nodes priv fields

Integrations

- [ ] Shelter on android
  - Shelter profile based routing
- Distributed firewalls
  - [ ] crowdsec
  - [ ] diswall

Adblock

- [ ] File format for compact and cheap storing of large FQDN and Ip lists
  - Intended for ad blocking but can be used in any node that use hostname/ip
    filters
- [ ] Dedicated adblock node for DNS, Tun and Network links
  - Adblocking can be implemented with already existed nodes but it would be
    nice to have dedicated node
  - [ ] Builin block lists fetcher & updater

DNS

- [x] meshname
- [x] DoT
- [x] DNS graph nodes and cache management
- [ ] DoH
- [ ] ODoH
- [ ] DoQ
- [ ] mDNS?
- [ ] Add DNS hijack node
  - Can be implemented with already existed nodes but it would be nice to have
    dedicated node
  - Should work with Tun and Network links

TLS

- [x] Warnings for hazard certs
- [x] Client TLS trust and revocation management
- [ ] Server side configs management
- [x] Manually add ru gov certs to hazard certs list
  - Manual sources have higher priority than parsed sources

DPI

- [x] Sniffer
- [ ] Reverse sniffer
- [ ] UDP protocols support
- More protocols classifiers

Secrets system

- [x] DB secrets storage
- Use secrets for
  - [x] WG priv key
  - [x] SOCKS client, server, and local proxy passwords (optional)

WireGuard

- [x] AmnesiaWG extension
- [x] WireGuard nodes and TUN route propagation
- [ ] Standard configs format import
- [ ] Something like <https://github.com/alex14fr/wgsig> or
  <https://github.com/malcolmseyd/natpunch-go>
- [ ] <https://github.com/ClusterM/wg-obfuscator>

Proxy/VPN protocols support

- Socks
  - [x] Client
    - [x] Noproxy string field
    - [ ] DNS port for tor resolve extension
  - [x] Server
- [x] Local proxy with filtering and secret-backed passwords
- [ ] SSH
  - [ ] Sshutle like?
  - [ ] Port passing
  - [ ] Shell pipe
- [ ] PPTP
- [ ] Shadowsocks
- [ ] Nebula
  - <https://github.com/slackhq/nebula>
  - <https://github.com/forgekeep/nebula-mesh>
- [ ] Tink?
- [ ] NaiveProxy
- [ ] Stunnel / [go-tunnel](https://github.com/opencoff/go-tunnel)
- [ ] iodine?
- [ ] Cloak?
- [ ] <https://github.com/jpillora/chisel> ?

Services clients

- [x] tailscale
- [x] amnesia
- [ ] netbird?
- ngrok-like
  - [ ] Ngrok itself
  - [ ] <https://github.com/andydunstall/piko>

Tor integration

- [x] Sandboxed Tor driver and onion services
- [ ] <https://github.com/cretz/bine>

DPI bypass tools

- <https://github.com/ValdikSS/GoodbyeDPI>
- <https://github.com/SadeghHayeri/GreenTunnel>
- <https://github.com/dilluti0n/dpibreak>
- <https://github.com/xvzc/spoofdpi>

Etc

- [ ] Multiplexer
  - <https://github.com/xtaci/smux>
  - <https://github.com/hashicorp/yamux>
- [ ] Reverse port mapping
  - Extra check for avoiding cycles
- [ ] Reverse proxy ( TCP/UDP/HTTP(S) )
- [ ] Node with proxychains like proxy chaining
- [ ] Bad network node
  - jitter, delays, etc
- [ ] Dispatcher node
  - round robin, random, etc
- [ ] Rate limit node
  - Tun and Network versions
  - for both outgoing (dial) and incoming (listen+accept) connections
- [ ] UDP over TCP
  - <https://github.com/mullvad/udp-over-tcp>
  - <https://github.com/jonhoo/udp-over-tcp>
  - <https://sing-box.sagernet.org/configuration/shared/udp-over-tcp/>
- [ ] <https://github.com/norouter/norouter>
- [ ] Shell callback for IP/Subnet filtering
- [ ] UpNp?

Sysnet

- [x] Sysnet linux
- [x] Sysnet windows
- [x] Windows daemon and named-pipe admin transport
- [ ] Drop resolved dns caches after changing it's configuration
- [ ] Rule for allowing direct pings
- More rules options for pre-filter and routing
  - [ ] dst addr
  - [ ] flatpak app?
  - [ ] docker container?
  - [ ] VM?
- [ ] Allow all sysnet features emulation via debug admin api

Admin

- [x] Admin API, graph panel, and local CLI transport
- [ ] Authorisation
  - [ ] Pssword/access tokens/fido(?)
  - [ ] Narow scoped access tokens for api clients

Cli

- [x] CLI graph, DNS, TLS, secrets, and snapshot commands
- [ ] Machine-readable json output by default when output is not a tty
- [ ] Semi-interactive tui mode (fzf-like pickers, etC) when output is a tty

Propagation

- lists
  - awesome\*
  - <https://research.torproject.org/tools/>

Plugin system?

- Go plugins vs IPC vs WASM

Runtime isolation

- [x] Linux daemon process isolation
- [x] Cross-platform subprogram sandboxing
