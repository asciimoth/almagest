Almagest replaces a collection of routes, tunnel tools, proxy services, DNS
services, and platform-specific rules with one local service. You build traffic
paths in one graph and use the same model on each supported platform.

Almagest includes a complete userspace TCP/IP stack. A graph can convert raw IP
packets to socket operations and back. This makes it possible to connect system
tunnels, VPNs, proxies, filters, and protocol services even when they operate at
different network layers.

> [!CAUTION]
> Almagest is pre-alpha software. Features, configuration, and documentation can
> change. Do not treat it as an audited security boundary or use it in
> production.

## What you can build

One Almagest graph can contain several independent or connected traffic paths.
For example, you can:

- Route traffic through different VPNs and proxies. Rules can match a
  destination, domain name, application, user, or group.
- Use [LocalProxy](@docs/node/almagest/LocalProxy) to provide a local proxy for
  a VPN, or use [Spoofer](@docs/node/almagest/Spoofer) to convert system TUN
  traffic to Network operations and send them through a proxy.
- Put a VPN connection through a proxy, put a proxy through a VPN, or build a
  chain of multiple [proxy clients](@docs/node/almagest/SocksClient).
- Combine DNS sources, fallbacks, scoped caches, and rules that send different
  questions to different networks.
- Use [Sniffer](@docs/node/almagest/Sniffer) to inspect and filter traffic by
  protocol or protocol data, such as an HTTP URL, TLS SNI, or TLS ALPN.
- Terminate or intercept TLS connections that you are authorized to inspect.
- Combine values, variables, gates, and loops as general-purpose logic, then use
  [Select](@docs/node/pasta/Select) nodes to change a traffic path while the
  graph runs.

Read [Network, TUN, DNS, and Object](@docs/almagest/types) for the main traffic
types. [DNS](@docs/almagest/dns), [TLS](@docs/almagest/tls), and
[application and process filtering](@docs/almagest/process-filtering) explain
the related features. [Values and logic](@docs/almagest/logic) explains the
available control nodes and the current limits of dynamic selection.

## The graph controls access

Each operation, client, service, and traffic transformation is a node. Typed
ports show what a node can receive or provide. Links connect compatible ports
and give each node an explicit path to the resources that it needs.

Only a small set of boundary nodes communicates with host applications or the
outside network. The main boundary nodes are
[OutNetwork](@docs/node/almagest/OutNetwork),
[OutDns](@docs/node/almagest/OutDns), [Tun](@docs/node/almagest/Tun),
[DefaultTun](@docs/node/almagest/DefaultTun), and
[LocalProxy](@docs/node/almagest/LocalProxy). They provide direct host
networking and DNS, create TUN interfaces, capture default traffic, or listen
for local proxy clients. Other network nodes communicate through their ports. An
isolated node or subgraph that has no path to a boundary node cannot send
network traffic to the host or the outside network.

This explicit model lets you control the input and output of each service. You
do not have to put the traffic from every service into one operating-system
network stack. A graph can contain separate traffic paths that do not overlap.
It can also contain a fully virtual subgraph for tests and debugging.

Read [Nodes, ports, and links](@docs/almagest/graph) for graph rules. Open the
reference page for a node to learn its exact inputs, outputs, and settings.

## A safe first graph

Start with a graph that does not change host routes. This graph sends one HTTP
request through the normal host network:

```graph
{
  "Outside Network Gateway": {
    "Class": "almagest/OutNetwork",
    "Pos": "{\"x\":475,\"y\":120}",
    "Label": "internet",
  },
  "HTTP Client": {
    "Class": "almagest/HttpClient",
    "Pos": "{\"x\":230,\"y\":120}",
    "Links": ["Network -> [Outside Network Gateway] Network"],
  },
}
```

[OutNetwork](@docs/node/almagest/OutNetwork) supplies the service's direct host
network. [HttpClient](@docs/node/almagest/HttpClient) uses that Network. Open
the HTTP Client menu, set a URL, and send a request.

Use [packaged examples](@docs/almagest/snapshots) for larger working graphs.
Examples start with isolated or narrow tests before they change host routing.

## Work in the panel

The Graph tab is the main editor. Add nodes from the node list, then connect
ports that have the same type. Select a node to open its settings and status.
The service saves accepted graph and menu changes to the editable configuration.

Use the other tabs for related data and status:

- **Secrets** stores credentials and other sensitive values for nodes.
- **DNS** shows the DNS cache stores.
- **TLS** manages certificates, policies, and the built-in certificate
  authority.
- **KV** manages named key-value stores.
- **Processes** shows active workers grouped by node. It also shows service
  isolation and subprogram sandbox status.
- **Logs** shows a limited recent log history and new log messages.
- **Config** shows the editable HuJSON configuration.
- **Snapshots** stores or restores the editable configuration and runtime file
  storage. It also lists packaged examples.
- **Docs** opens these pages and the reference page for each node.

For terminal administration or foreground standard-input use, read
[Service and command line](@docs/almagest/daemon-cli).

Most configuration and stored state is available in the panel. Some dangerous
host-level options are in a separate protected configuration. The panel can use
the resulting policy, but it cannot read or change this configuration. A host
administrator controls it. Read
[Configuration, KV, and secrets](@docs/almagest/data) for this separation and
[Isolation and sandboxing](@docs/almagest/security) for the worker security
status.

## Before you route host traffic

[DefaultTun](@docs/node/almagest/DefaultTun) can change routes and DNS settings
for the complete host. First test the path with clients inside the graph. Then
capture a narrow test prefix. Add default routes only after both tests succeed.

Use strict routing or a [kill switch](@docs/almagest/killswitch) when traffic
must not fall back to the normal network.

Continue with [Architecture](@docs/almagest/architecture) to learn how the
service, panel, command line, configuration, and traffic paths fit together.
