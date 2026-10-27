`almagestd` is the Almagest background service. It owns the live graph,
networking resources, configuration, stored data, and administration API. A
normal installation starts it as a privileged system service. It stays active
when you close the desktop app, close the web panel, or disconnect the command
line.

The desktop app, web panel, and `almagest-cli` control `almagestd`. They do not
carry or process graph traffic. Multiple administration clients can connect at
the same time. Connecting or disconnecting a client does not stop the graph.

## Administration interfaces

The desktop app provides the same graphical interface as the web panel. It
connects through a native operating-system channel, such as a Unix socket or a
Windows named pipe. The operating system applies the access rules for this
channel. Use the desktop app for normal interactive administration.

`almagest-cli` also uses a native channel by default. It is suitable for
terminal use, scripts, diagnostics, and recovery. Read
[Service and command line](@docs/almagest/daemon-cli) for its commands and for
the foreground standard-input mode of `almagestd`.

`almagestd` can also provide a local web admin panel. You can open this panel in
a browser. For a service on another host, you can forward its local web listener
through SSH and open the forwarded address in a local browser.

Prefer the desktop app or `almagest-cli` when possible. Their native channels
can use stronger operating-system access controls. The web listener has no
general browser authentication. Optional user and group filters can limit which
local processes connect, but these filters are best-effort checks. Do not use
them as a security boundary. Do not expose the web listener to an untrusted
network. Read [System administration](@docs/almagest/administration) for
endpoint and access-control details.

## Control path and traffic path

The administration API is the control path. It carries graph edits, status,
logs, and stored data between `almagestd` and an administration client. Access
to this API gives full control of Almagest.

Network and TUN links in the graph form traffic paths. These paths stay in
`almagestd`; they do not pass through an administration client. Thus, closing an
administration interface does not stop network traffic. Read
[Nodes, ports, and links](@docs/almagest/graph) for the graph model and
[Network, TUN, DNS, and Object](@docs/almagest/types) for the main link types.

## Configuration and stored data

The service has one graph. The regular configuration describes its nodes, links,
and editable service options. Separate file storage contains data such as KV
values, secrets, DNS caches, TLS material, and
[snapshots](@docs/almagest/snapshots).

The panel and other administration clients can read and change regular
configuration. Protected configuration contains host administrator policy.
Administration clients cannot read or change it.

This separation prevents a graph edit from granting new host access to itself.
For example, a [TorDriver](@docs/node/almagest/TorDriver) node can request an
approved `tor` program, but it cannot select an executable path or arbitrary
command arguments.

Read [Configuration, KV, and secrets](@docs/almagest/data) for the data model.

## Privileged operations

Some operations need operating-system privileges. These operations include
creating a TUN interface, changing routes or DNS settings, tracking processes,
and starting restricted third-party programs. Running `almagestd` as a system
service gives it the required access even when no user is signed in.

Almagest has two separate restriction systems:

- **Service isolation** splits service duties into restricted processes.
- **Subprogram sandboxing** restricts third-party programs that graph nodes
  start, such as Tor.

The systems have different modes and platform support. Read
[Isolation and sandboxing](@docs/almagest/security) before you depend on them.
