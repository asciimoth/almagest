A snapshot is a partial backup of the Almagest configuration and persistent
state. Use snapshots before large graph changes, to keep known configurations,
or to move a configuration and its data to another Almagest service.

You can create and restore snapshots through the administration API while the
service runs. A restore replaces the active data and rebuilds the graph. You do
not have to restart the service. Existing traffic and other work in the old
graph can stop when its nodes are replaced.

## Snapshot contents

A snapshot includes:

- the complete regular configuration, including the graph and editable service
  options;
- mutable KV data and secrets;
- mutable DNS cache and TLS data; and
- other persistent files that nodes keep in Almagest file storage.

The exact stored data depends on the nodes and features that the graph uses.

> [!WARNING]
> Snapshots include mutable secrets. They can also include private TLS keys and
> other credentials. Protect exported snapshot archives as you protect the
> credentials themselves.

A snapshot does not include:

- protected configuration or protected secrets and TLS material;
- other snapshots stored by the service;
- service command-line options, environment variables, or operating-system
  service settings;
- files and third-party program data outside Almagest file storage; or
- live connections, running process state, and in-memory logs.

Protected configuration stays active when you restore a snapshot. Host policy
can therefore change how a restored graph works. A snapshot is not a complete
host backup and does not replace an administrator backup of protected
configuration and service settings.

Read [Configuration, KV, and secrets](@docs/almagest/data) for the difference
between regular, mutable, and protected data.

## Create and restore snapshots

Use the [Snapshots tab](@panel/snapshots) to manage snapshots:

1. Enter an optional name and select **Create snapshot**.
1. Make the configuration or stored-data changes that you want to test.
1. To return to the saved state, find the snapshot and select **Restore**.

A name is a label and does not identify the snapshot. More than one snapshot can
have the same name.

Restore replaces the current regular configuration and mutable persistent data.
The snapshot list itself is not replaced. If the service cannot write the
restored regular configuration to its configured file, the panel reports that
the restored configuration is active only in memory. In that case, do not expect
the configuration to remain after a service restart.

You can also rename and delete stored snapshots. Deletion is permanent unless
you exported another copy.

## Export and import archives

Export writes one snapshot as a ZIP archive. Import adds a snapshot archive to
the service snapshot list. Import does not restore it automatically. Select
**Restore** after import when you want to make its state active.

Use archives to keep backups outside the service or to transfer state between
Almagest installations. The receiving installation still uses its own protected
configuration and host settings. Differences in platform support, available
programs, and administrator policy can prevent parts of an imported graph from
starting.

The `almagest-cli snapshots` commands provide the same create, list, restore,
export, import, rename, and delete operations. Read
[Service and command line](@docs/almagest/daemon-cli) for administration CLI
usage.

## Examples

Examples are read-only snapshots packaged with Almagest. They provide working
graphs for [graph basics](@docs/almagest/graph),
[Network and TUN paths](@docs/almagest/types), [DNS](@docs/almagest/dns),
[TLS](@docs/almagest/tls), [WireGuard](@docs/node/almagest/WireGuard),
[Tor](@docs/node/almagest/TorDriver),
[filtering](@docs/almagest/process-filtering), and protocol routing. Each
example includes a short description and instructions for an action that you can
try.

To load an example:

1. Open the [Snapshots tab](@panel/snapshots).
1. Find the **Examples** section.
1. Select an example and choose **Load**.
1. Read its configuration comments, then return to the graph.

Loading an example uses the snapshot restore process. It replaces the active
regular configuration and mutable persistent data. Create a snapshot first if
you want to return to the current state.

Examples themselves are read-only. You cannot rename, delete, or replace a
packaged example. After you load one, you can change the active graph and save
the result as a new snapshot.

Start with **Hello Network** or **Mock Network**. These examples do not change
host routing. Continue in numeric order for a gradual introduction to more node
classes.

Examples that create a host TUN or use
[DefaultTun](@docs/node/almagest/DefaultTun) need platform support and
sufficient service privileges. Some examples also need protected administrator
policy. For example, **Browse through Tor** needs an approved Tor program. The
graph cannot add this permission.

You can inspect the exact HuJSON on the [Config tab](@panel/config). Use the
node reference pages to learn about one node in detail.
