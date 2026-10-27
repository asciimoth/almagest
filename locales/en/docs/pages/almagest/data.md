Almagest keeps graph configuration, administrator policy, and runtime data in
separate stores. Choose the store by purpose. Do not put every value in the
HuJSON configuration.

## Regular configuration

Regular configuration contains the graph and user-editable service settings. It
uses HuJSON, which permits comments and trailing commas. The panel can read and
change this document. Accepted graph edits are saved to it after a short idle
delay.

The top-level `nodes` object contains the main graph. `Config` contains general
service settings, and `Logs` contains the in-memory log-cache settings. Unknown
fields can be retained for compatibility, but a known field still must have a
valid value. [Nodes, ports, and links](@docs/almagest/graph) describes the saved
graph fields and link syntax.

Use the [Config tab](@panel/config) to review or apply the complete document. An
applied document can add, change, and remove live nodes and links. Create a
snapshot before a large edit. Read
[Snapshots and examples](@docs/almagest/snapshots) for snapshot contents and
restore behavior.

### Regular configuration options

The regular configuration has these top-level objects:

- `nodes` contains the saved graph. Each member name is the unique node name.
  `Class` selects its node class, `Primary` overrides its primary port type,
  `Pos` stores its graph position, and `Links` lists its outgoing links.
  Lowercase fields contain settings that belong to that node class.
- `Config.Save.IdleMilliseconds` sets how long the service waits after a change
  before it saves the configuration. The default is 500 milliseconds.
- `Config.TLS.BuiltinCA` controls generation of the built-in CA. `Issuer` sets
  its certificate name. `ValidityDays` sets its validity period, where `0`
  selects the no-practical-expiration fallback. `KeyAlgorithm` selects `ecdsa`,
  `ed25519`, or `rsa`. `Curve` selects `P256`, `P384`, or `P521` for an ECDSA
  key. `KeyBits` sets the RSA key size from 2048 through 8192 bits.
- `Logs.Cache` sets the five in-memory log limits. See
  [Logs and diagnostics](@docs/almagest/logs).
- `Protected` supplies protected configuration only in a combined document read
  with `almagestd --stdin`. Do not put it in the regular configuration file.

`Config.TLS.CRLs` is not a regular setting. Configure CRLs in protected
`TLS.CRLs` or with service startup options. See [TLS](@docs/almagest/tls).

## Protected configuration

Protected configuration contains system-administrator policy. It can select host
integration, administration listeners, immutable secrets, immutable TLS
material, external command policy, and sandbox programs.

The panel and config API never return protected configuration. A user who can
edit the graph cannot use regular configuration to grant a node a new program,
secret, host path, or privileged integration.

For normal service use, protected configuration is a separate file. When
`almagestd --stdin` reads the complete configuration from standard input, an
optional top-level `Protected` object supplies it instead.

Restart the service after you change a protected file.

[System administration](@docs/almagest/administration) lists the installed file
locations, ownership requirements, and protected host-integration options.

## KV stores

KV is persistent hierarchical key/value storage. The default store is shared.
Keys can hold text or binary data, and a value can be marked hidden.

Use KV for non-secret persistent application state. The KV interface supports
subtree creation, reads, updates, deletion, and change subscriptions. Hidden is
a presentation attribute, not an access-control boundary.

## Secrets

Secrets are named values with an allowlist of node names. A node can read a
secret only when its name is in that secret's allowlist. When you rename a node,
Almagest updates these references in the same operation.

Mutable secrets are stored in runtime file storage and can be managed from the
panel or CLI. Protected secrets come from protected configuration and override
mutable secrets with the same key. Protected values can be literal text or can
be read once from an environment variable at service startup.

In each protected `Secrets` entry, set exactly one source. `Value` contains the
literal value. `Env` names an environment variable that the service reads once
at startup. `Nodes` lists the node names that can read the secret. An empty
`Nodes` list gives no node access.

Administrators who can access the local administration API can view effective
secret values. Protect the administration socket or pipe accordingly.

Snapshots include mutable file-storage data, including mutable secrets. They do
not include protected configuration. Treat exported snapshots as sensitive.

## Which store to use

- Use regular configuration for graph structure and ordinary node settings.
- Use KV for persistent non-secret data that is not graph structure.
- Use mutable secrets for credentials managed through Almagest.
- Use protected secrets for administrator-supplied credentials that graph users
  must not replace.
