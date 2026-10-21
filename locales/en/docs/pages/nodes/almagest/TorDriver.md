TorDriver starts a separate [Tor](https://www.torproject.org/) client and supplies one or more Tor-backed
Networks. Use these Networks for outgoing TCP connections, or add a v3 onion
service for incoming TCP connections.

Connect a working Network to **out**. Tor uses this Network for its relay and
control connections. It does not use a direct host connection as a fallback.
Without this connection, the node waits for a Network and does not supply a
ready Tor Network. Avoid a routing loop in which **out** depends on a Network
from the same TorDriver.

The default configuration is enabled and has one client Network named
`default`. It supplies this Network as **Network default**. Each additional
client Network adds a port named **Network _name_**. Each onion service adds a
port named **Onion _name_**. Network and service names are not case-sensitive
and must be unique.

## Client Networks

A client Network supports outgoing TCP connections, forward IP lookups, and
reverse DNS lookups through Tor. Normal hostname lookups do not use the host
DNS resolver. UDP, multicast, and incoming listeners are not supported.

Do not resolve an `.onion` name to an IP address. Connect to the onion hostname
and port directly, for example `exampleonionaddress.onion:443`.

Each configured Network has a separate Tor isolation identity. **Circuit
isolation** controls when that identity changes:

- `default` and `session` reuse one isolation identity for the Network. Tor can
  still replace its circuits.
- `connection` uses a new isolation identity for each connection or DNS
  operation. This prevents that operation from sharing an already used circuit
  identity. It does not guarantee different relays.

For `default` or `session`, **Isolate destination address** and **Isolate
destination port** divide the reusable identity by destination. These options
cannot be used with `connection`.

The global **Client IP policy** controls the permitted or preferred IP address
families. **Onion traffic** can allow, require, or reject `.onion` destinations.
These policies apply to all client Networks from this node.

Application data can still identify or correlate a user across isolated
circuits. Circuit isolation is not a complete identity boundary.

## Onion services

An onion service supplies a listen-only Network. Connect a TCP server to
**Onion _name_** and make it listen on one of the configured **Virtual ports**.
The listener address is the generated or restored v3 onion address with that
virtual port. Outgoing connections, DNS operations, and UDP are not supported
on this Network.

Configure from 1 to 128 different, nonzero virtual ports for each service. The
**Maximum streams** value limits concurrent streams. Zero uses Tor's default
without a configured limit. With the `keep` policy, Tor rejects excess streams
but keeps the rendezvous circuit. With `close`, Tor closes that circuit when it
reaches the limit; this policy requires a nonzero limit.

Select **Private key secret** to keep an existing onion address. If you leave
it empty, TorDriver generates a key and saves it as a node secret after the
node configuration is saved. A deployment that cannot save the configuration
cannot generate and retain this secret. Select an existing secret in that
case.

Add one Tor-formatted public key per line to **Authorized client public keys**
to restrict the service. A service with this list accepts only clients that
have a matching private authorization key. Keep the service private key and
client private keys secret.

The status shows the onion address and whether publication is pending,
published, or failed. A listener can start before publication completes, but
remote clients cannot connect until Tor publishes the service descriptor.

## Access to protected onion services

Use **Client authorization** to let the client Networks access a protected v3
onion service. Enter its 56-character onion address, with or without `.onion`,
and select the secret that contains the matching private authorization key.
You can optionally set a key name of up to 16 letters, digits, `+`, `-`, or
`_` characters.

If **Private key secret** is empty, TorDriver generates and securely saves a
new private authorization key. The onion service must be configured with the
corresponding public key before that key can grant access.

## Bridges and relay policy

Enable **Use bridges** and add at least one **Bridge line** to connect through
bridges. A bridge address must be a numeric IP address and a nonzero port.
Optional relay fingerprints contain exactly 40 hexadecimal characters.

For an obfs4 bridge, also enable **Enable obfs4** and supply its fingerprint,
certificate, and inter-arrival timing setting. Host env must provide the
optional obfs4 program. A missing obfs4 program does not prevent normal Tor or
plain bridges from working, but an obfs4 configuration cannot start without
it.

The policy section also controls bandwidth, padding, reachable relay ports,
pending circuits, CPU count, and relay selection. Set **Bandwidth rate** and
**Bandwidth burst** together; the burst must not be less than the rate. Relay
fingerprints in the entry, exit, and exclusion fields must each contain 40
hexadecimal characters. **Strict relay selection** enforces the configured
relay choices instead of treating them as preferences.

Relay restrictions and disabled padding can reduce privacy or make Tor unable
to connect. Use these settings only when the network policy requires them.

**Outbound failure policy** controls an error from the Network on **out**.
`default` and `retry` retry recoverable failures. `latch` keeps the node
unavailable after a failure until you replace or reconnect that Network.

## Process security and platform support

TorDriver cannot select an executable, pass arbitrary command-line arguments,
run shell commands, or add raw Tor configuration. It requests a fixed Tor
program from the daemon's protected sandbox configuration. obfs4 uses a
separate fixed program permission. The administrator controls the executable,
fixed arguments, storage, resource limits, identity, and operating system
isolation.

Automatic system-program discovery is off by default. An administrator can
enable it in the protected daemon configuration or with
`--discover-system-programs`. It searches for Tor and, when available,
`lyrebird` or `obfs4proxy`. Explicit protected program configuration takes
priority. If Tor is missing or its protected profile is invalid, the daemon
continues to run and TorDriver reports the error.

On supported Linux systems, automatic `auto` and `strict` profiles can use the
full Linux process boundary. On Windows, they use a restricted token and Job
Object; `strict` fails if a required protection cannot be applied. Automatic
profiles on other systems use direct execution. Sandbox mode `off` also uses
direct execution. Review the daemon's protected sandbox policy before you use
TorDriver with sensitive traffic.

The Tor exit relay can read unencrypted traffic to ordinary Internet
destinations. Use end-to-end TLS where possible. The Network connected to
**out** can observe Tor relay connections and traffic patterns. Tor reduces
direct destination exposure, but it does not prevent all traffic analysis.

Changing accepted configuration restarts the Tor client and can close active
connections and listeners. Detaching **out** makes the node unavailable.
Reconnect it to restore Tor connectivity. Use **Restart Tor** in the menu to
restart the current configuration explicitly.
