Fixes
- [ ] Find out why there is so much workers respawn in daemon logs
    - Why there is so much dns query workers
- [ ] Fix work after host sleep wakeup

Optimisation
- [ ] Compress builtin admin pamel
- [ ] Switch to other userspace TCP/IP stack implementation
    - https://github.com/soypat/lneto ?

Dev
- [ ] Better web dev setup (linters, etc)
- [ ] SBOM

Docs
- [ ] Guide for porting to new platforms
- [ ] Rich docs rendering with graph examples

Tests
- [ ] Add undo/redo tests analog for all existed topology tests

UX/QoL
- [ ] Make sure there are example placeholders in all node menus fields
- [ ] Dedicated admin panel tab for monitoring of all DNS requests in graph
- [ ] Review log levels

Configuration
- [ ] Config templates system for setups with immutable template and mutable config
- [ ] Hujson config editing in admin panel
- [ ] Config snapsots system with snapshot/rollback options in admin panel
- [ ] Omitempty/omitdefault in nodes priv fields

Integrations
- [ ] Shelter on android
    - Shelter profile based routing
- Distributed firewalls
    - [ ] crowdsec
    - [ ] diswall

Adblock
- [ ] File format for compact and cheap storing of large FQDN and Ip lists
    - Intended for ad blocking but can be used in any node that use hostname/ip filters
- [ ] Dedicated adblock node for DNS, Tun and Network links
    - Adblocking can be implemented with already existed nodes but it would be nice to have dedicated node
    - [ ] Builin block lists fetcher & updater

DNS
- [X] meshname 
- [X] DoT
- [ ] DoH
- [ ] ODoH
- [ ] DoQ
- [ ] mDNS?
- [ ] Add DNS hijack node
    - Can be implemented with already existed nodes but it would be nice to have dedicated node
    - Should work with Tun and Network links

TLS
- [X] Warnings for hazard certs
- [ ] Server side configs management
- [ ] Manually add ru gov certs to hazard certs list
    - Make sure manual source have higher prior than parsed one

DPI
- [X] Sniffer
- [ ] Reverse sniffer
- [ ] UDP protocols support
- More protocols classifiers

Secrets system
- [X] DB secrets storage
- Use secrets for
    - [X] WG priv key
    - [ ] socks client/server password (optional)

WireGuard
- [X] AmnesiaWG extension
- [ ] Standard configs format import
- [ ] Something like https://github.com/alex14fr/wgsig or https://github.com/malcolmseyd/natpunch-go
- [ ] https://github.com/ClusterM/wg-obfuscator

Proxy/VPN protocols support
- Socks
    - [X] Client
        - [X] Noproxy string field
    - [X] Server
- [ ] SSH
    - [ ] Sshutle like?
    - [ ] Port passing
    - [ ] Shell pipe
- [ ] PPTP
- [ ] Shadowsocks
- [ ] Nebula
    - https://github.com/slackhq/nebula
    - https://github.com/forgekeep/nebula-mesh
- [ ] NaiveProxy
- [ ] Stunnel / [go-tunnel](https://github.com/opencoff/go-tunnel)
- [ ] iodine?
- [ ] Cloak?
- [ ] https://github.com/jpillora/chisel ?

Services clients
- [ ] tailscale
- [ ] amnesia
- [ ] netbird?
- ngrok-like
    - [ ] Ngrok itself
    - [ ] https://github.com/andydunstall/piko

Tor integration
- [ ] https://github.com/cretz/bine

DPI bypass tools
- https://github.com/ValdikSS/GoodbyeDPI
- https://github.com/SadeghHayeri/GreenTunnel
- https://github.com/dilluti0n/dpibreak
- https://github.com/xvzc/spoofdpi

Etc
- [ ] Multiplexer
    - https://github.com/xtaci/smux
    - https://github.com/hashicorp/yamux
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
- [ ] https://github.com/norouter/norouter
- [ ] Shell callback for IP/Subnet filtering
- [ ] UpNp?

Sysnet
- [X] Sysnet linux
- [ ] Sysnet windows
- [ ] Drop resolved dns caches after changing it's configuration
- [ ] Rule for allowing direct pings
- More rules options for pre-filter and routing
    - [ ] dst addr
    - [ ] flatpak app?
    - [ ] docker container?
    - [ ] VM?
- [ ] Allow all sysnet features emulation via debug admin api

Admin
- [ ] Authorisation
    - [ ] Pssword/access tokens/fido(?)
    - [ ] Narow scoped access tokens for api clients

Cli
- [ ] Machine-readable json output by default when output is not a tty
- [ ] Semi-interactive tui mode (fzf-like pickers, etC) when output is a tty


Plugin system?
- Go plugins vs IPC vs WASM
