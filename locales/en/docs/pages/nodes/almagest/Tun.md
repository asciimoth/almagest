Creates a regular TUN interface on the daemon host and supplies it to one TUN
consumer. Use it when a node such as WireGuard must read and write IP packets
through a host interface.

This node does not automatically make the interface the system default route.
Add only the routes that must use the interface. Use DefaultTun instead when
you need Almagest to manage system-wide default routing and application
selection.

The node is available only when the daemon reports that it can create regular
TUN interfaces. Linux and Windows have native support, but availability still
depends on the daemon configuration, required drivers, and host permissions.
If the active platform
cannot create the interface, the node is not shown in the node list.

## Interface settings

**Name** requests a host interface name. Leave it empty to let the platform
select a name. Name rules and support are platform-specific. The menu disables
editing for this field when the active platform cannot create a named
interface.

**TUN addrs** assigns IP prefixes to the interface. Add one value per entry,
for example:

- `10.20.0.2/32`
- `fd00:20::2/128`

**TUN routes** adds destination prefixes that the host sends through the
interface. For example, `10.30.0.0/16` routes that subnet through the TUN.
Routes such as `0.0.0.0/0` or `::/0` can capture most host traffic. Use them
only when the connected TUN consumer can forward that traffic and you have a
recovery path.

**MTU** sets the interface MTU in bytes. Zero lets the platform select a
default. A negative value is invalid. Positive values must be within the range
supported by the active platform and address families.

The **Effective** section shows the name, addresses, routes, and MTU options
that the node has applied. The node label shows the effective interface name
when one is set. Otherwise, it shows `ready` after successful creation.

## Settings suggested by the TUN consumer

A connected consumer can suggest interface addresses, routes, and an MTU. The
node combines these suggestions with the menu settings:

- Suggested addresses and routes are appended to the configured lists.
  Exact duplicate values are removed.
- If the menu and the consumer both specify a positive MTU, the smaller value
  is used.
- A zero MTU does not override a positive value.

The Effective section includes the merged result. Disconnecting the consumer
removes its suggestions and restores the menu settings.

The node ignores DNS and source route suggestions. Use DNS nodes for DNS
routing and policy. Use a TUN provider that supports source routing when one
interface needs different source addresses for different destinations.

## Updates and errors

When the platform supports a live change, the node updates the existing
interface. Otherwise, it closes the interface and creates a replacement.
Traffic can stop briefly, and the connected consumer receives the replacement
TUN. Changing the requested name can require replacement on platforms that do
not support live rename.

The node keeps the current configuration when a menu edit fails validation.
Its label changes to `invalid config`. If interface creation or replacement
fails, the old interface is closed, the TUN becomes unavailable, and the label
changes to `error`. Apply a valid configuration again after you correct the
host or platform problem.
