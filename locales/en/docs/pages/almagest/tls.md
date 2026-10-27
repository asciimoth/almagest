TLS authenticates a peer and encrypts a connection. A certificate binds a public
key to hostnames, IP addresses, or another identity. A certificate authority
(CA) signs certificates. A client accepts a server certificate only when the
applicable verification policy accepts its chain and identity.

Almagest stores certificates, verification policies, and its built-in CA in
**TLS configs**. TLS-capable nodes use these configs for their client or server
TLS settings. Most nodes that show a **TLS config** selector use the built-in
`default` config when you do not select another one.

Open the **TLS configs** tab in the administration interface to inspect the
built-in CA, add certificates, and create named verification policies. A named
policy then becomes available in the **TLS config** selector of applicable
nodes.

Examples include [HttpClient](@docs/node/almagest/HttpClient), encrypted DNS in
[DnsClient](@docs/node/almagest/DnsClient), TLS proxy connections in
[SocksClient](@docs/node/almagest/SocksClient), and the upstream connection from
[TlsMitm](@docs/node/almagest/TlsMitm).
[TlsWrapper](@docs/node/almagest/TlsWrapper) can add TLS to a plaintext client
or server. Other server-side TLS nodes use the built-in CA to create the
certificates that they present.

## Certificates and verification policies

A certificate entry contains a certificate that a policy can trust or restrict.
A verification policy selects trust anchors and can restrict their authority to
specified DNS names or IP ranges. It can also require a specified certificate or
public-key fingerprint for a scope.

These restrictions are useful when you must trust a third-party CA but do not
want to give it general authority. For example, you can permit a government CA
only for the government's domains and address ranges. A certificate from that CA
then cannot validate an unrelated site through this policy. Normal server-name
verification still applies.

You can pin a certificate or public key to selected hosts or addresses. The
policy then requires its fingerprint in that scope. Pinning is a restriction on
how the certificate or key can be trusted. It is not an independent certificate
source.

The `default` config uses the operating-system trust data. Almagest reads that
data when the service starts. Use **Reload OS certs** or the matching CLI
command after the system trust store changes.

This graph applies an Almagest verification policy to selected TLS connections.
TlsMitm verifies the remote server with its selected TLS config. It then gives
the local application a new certificate from the built-in CA.

```graph
{
  "Local HTTPS Client": {
    "Class": "almagest/HttpClient",
    "Pos": "{\"x\":31,\"y\":120}",
    "Links": ["Network -> [TLS Policy Enforcement] Network"],
    "url": "https://service.example/",
  },
  "TLS Policy Enforcement": {
    "Class": "almagest/TlsMitm",
    "Pos": "{\"x\":245,\"y\":120}",
    "Links": ["Network -> [Outside Network] Network"],
    "filterMode": "inclusive",
    "filterRules": "[{\"sni_hosts\":[\"service.example\"]}]",
    "tlsConfig": "restricted-service-policy",
  },
  "Outside Network": {
    "Class": "almagest/OutNetwork",
    "Pos": "{\"x\":498,\"y\":120}",
  },
}
```

If local applications trust the built-in CA, this pattern can enforce a stronger
TLS policy for applications that do not have that feature. Apply it only to the
intended destinations. Applications with their own certificate pins can reject
the certificate that Almagest presents.

## Add, remove, or inspect TLS

TLS nodes are Network middleware. You can put them at a precise point in a
traffic path instead of changing every client or server.

- TlsWrapper adds TLS to plaintext connections. It can act as a TLS client for
  outgoing connections or as a TLS server for listeners.
- [TlsTerminator](@docs/node/almagest/TlsTerminator) accepts TLS from an
  outgoing client and sends plaintext to the destination.
- TlsMitm ends the TLS connection from a client and creates a separate TLS
  connection to the original server. It can inspect selected connections and
  apply an Almagest verification policy to the remote server.

This isolated example adds TLS between a plaintext HTTP client and server, then
removes it again. The HTTP nodes do not need their own TLS configuration.

```graph
{
  "Plain HTTP Client": {
    "Class": "almagest/HttpClient",
    "Pos": "{\"x\":23,\"y\":120}",
    "Links": ["Network -> [TLS Wrapper] Network"],
    "url": "http://service.example:8080/",
  },
  "TLS Wrapper": {
    "Class": "almagest/TlsWrapper",
    "Pos": "{\"x\":199,\"y\":120}",
    "Links": ["Network -> [TLS Terminator] Network"],
    "clientServerName": "service.example",
    "clientAlpn": "http/1.1",
  },
  "TLS Terminator": {
    "Class": "almagest/TlsTerminator",
    "Pos": "{\"x\":383,\"y\":120}",
    "Links": ["Network -> [Private Network] Network"],
    "serverAlpn": "http/1.1",
  },
  "Plain HTTP Server": {
    "Class": "almagest/HttpServer",
    "Pos": "{\"x\":383,\"y\":227}",
    "Links": ["Network -> [Private Network] Network"],
  },
  "Private Network": {
    "Class": "almagest/MockNetwork",
    "Pos": "{\"x\":598,\"y\":173}",
  },
}
```

## Provide HTTPS for a plaintext private service

You can present a plaintext service to local applications as HTTPS. This is
useful when the path to the service already has suitable protection, but a local
browser or another client requires HTTPS. In this use case, Almagest provides a
TLS facade, or mocks the TLS layer, for the private service.

In the next graph, the local client opens an HTTPS connection. TlsTerminator
presents a certificate from the built-in CA and converts the connection to
plaintext HTTP. The HTTP connection then travels inside WireGuard. The remote
service stays a plaintext HTTP service, while the local client sees HTTPS and
can verify the Almagest certificate without a warning.

```graph
{
  "Local HTTPS Client": {
    "Class": "almagest/HttpClient",
    "Pos": "{\"x\":15,\"y\":53}",
    "Links": ["Network -> [HTTPS Facade] Network"],
    "url": "https://private.example:8080/",
  },
  "HTTPS Facade": {
    "Class": "almagest/TlsTerminator",
    "Pos": "{\"x\":184,\"y\":53}",
    "Links": ["Network -> [Client IP Stack] Network"],
    "serverAlpn": "http/1.1",
  },
  "Client IP Stack": {
    "Class": "almagest/VTun",
    "Pos": "{\"x\":360,\"y\":53}",
    "Links": ["Tun -> [Client WireGuard] Tun"],
  },
  "Client WireGuard": {
    "Class": "almagest/WireGuard",
    "Pos": "{\"x\":537,\"y\":53}",
    "Links": ["Network -> [WireGuard Transport] Network"],
  },
  "WireGuard Transport": {
    "Class": "almagest/MockNetwork",
    "Pos": "{\"x\":728,\"y\":147}",
  },
  "Server WireGuard": {
    "Class": "almagest/WireGuard",
    "Pos": "{\"x\":537,\"y\":240}",
    "Links": ["Network -> [WireGuard Transport] Network"],
  },
  "Server IP Stack": {
    "Class": "almagest/VTun",
    "Pos": "{\"x\":360,\"y\":240}",
    "Links": ["Tun -> [Server WireGuard] Tun"],
  },
  "Plain HTTP Service": {
    "Class": "almagest/HttpServer",
    "Pos": "{\"x\":169,\"y\":240}",
    "Links": ["Network -> [Server IP Stack] Network"],
  },
}
```

The graph is a topology preview. A working WireGuard path also needs addresses,
keys, peers, and routes. See [WireGuard](@docs/node/almagest/WireGuard) and
[VTun](@docs/node/almagest/VTun) for those settings.

Install only the public built-in CA certificate in the local trust store. Do not
export or install its private key. Use this pattern only when the path after TLS
termination has the confidentiality, authentication, and access controls that
the service requires.

## Built-in CA

Almagest generates and stores a built-in CA by default. Nodes use it to issue
short-lived certificates for TLS termination, wrapping, and interception. A
client must trust the public certificate of this CA before it can accept these
generated certificates.

Keep the CA private key secret. A [snapshot](@docs/almagest/snapshots) can
contain mutable TLS state, so protect snapshot archives. A host administrator
can instead supply an immutable CA key pair through
[protected configuration](@docs/almagest/data). This protected CA is not
exported in a snapshot and cannot be regenerated through the normal TLS
interface.

Adding the built-in CA to the operating-system trust store gives it broad power.
Prefer a separate application trust store when possible. If you use system
trust, limit interception routes and use scoped upstream policies. Remove the CA
from system trust when applications no longer need it.

## Protected TLS option reference

Protected `TLS` entries are read at service startup and override mutable entries
with the same names:

- `BuiltinCA` supplies one immutable CA certificate and private key. Set either
  `PEM` to their combined PEM text or `File` to a PEM file path.
- `Certificates` maps names to immutable CA certificates. For each certificate,
  set either `PEM` to one certificate or `File` to a file that contains one
  certificate.
- `Policies` maps names to server-verification policies. `trust_anchors` lists
  trusted certificate names and their `scope`. `constraints` restricts a named
  certificate or public key with `cert`, `match`, and `scope`. `pins` requires a
  `fingerprint` of the selected `kind` in its `scope`. The `match` and `kind`
  values are `certificate` or `spki`.
- A policy `scope` uses `any`, `dns`, or `ip_prefixes`. Each `dns` rule has a
  `domain` and an optional `include_subdomains` switch. `ip_prefixes` contains
  IP address ranges. Do not combine `any` with the other scope fields.
- `CRLs` controls static certificate-revocation data as described in the next
  section.

Protected certificate and policy names replace mutable entries with the same
names. The names reserved for built-in and operating-system entries cannot be
used for protected certificates or policies.

## Certificate revocation lists

A certificate revocation list (CRL) identifies certificates that a CA revoked
before their normal expiration. CRL checking needs both the correct list and a
policy path that uses its issuer. A stale list can miss new revocations. An
unrelated list has no effect.

Static CRLs are a host-administrator feature. They are disabled until protected
configuration or service startup options enable them. The service reads each
configured PEM file once at startup. It does not download CRLs.

Protected configuration can enable named CRL files:

```hujson
{
  "TLS": {
    "CRLs": {
      "Enabled": true,
      "Lists": {
        "company-ca": {"File": "/etc/almagest/company-ca.crl.pem"},
      },
    },
  },
}
```

`TLS.CRLs.Enabled` enables CRL parsing and enforcement. `Lists` maps names to
sources. Each source sets either `File` to an absolute file path or `PEM` to
literal PEM data. `System.WindowsRevocation` selects `off` or `cache-only` for
Windows revocation data. The reserved `online` value is not implemented and is
rejected.

The equivalent startup options are repeatable:

```sh
almagestd --tls-crls \
  --tls-crl-file company-ca=/etc/almagest/company-ca.crl.pem
```

On Windows, protected configuration can also select cached operating-system
revocation data. The available mode is `cache-only`; online Windows revocation
is not available. Other platforms do not provide this system CRL mode.

The built-in CA also has an exportable built-in CRL. Distribute the CA and CRL
only to systems that must trust certificates issued by that CA.

## Interception warning

TLS interception ends one TLS connection and creates another. The intercepting
node can read and change plaintext. It can also break application certificate
pinning, mutual TLS, ECH, and protocols that depend on the original peer
certificate.

Use TlsMitm only on traffic that you are authorized to inspect. Limit its routes
and trust scope. A TLS verification policy controls which remote peers Almagest
trusts. It does not reduce the authority of the built-in CA after you install
that CA as a general system trust anchor.
