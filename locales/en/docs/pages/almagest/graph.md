A graph shows how nodes that produce, consume, or process traffic and data
connect to each other. Each node reacts independently when its configuration or
inputs change. Almagest does not restart or rebuild the complete graph for each
change.

In a complex graph, one node can affect the state of many other nodes. During
startup or a configuration change, some nodes can change between `up`, `down`,
and waiting states several times before the graph becomes stable.

## Nodes

A node is one configured instance of a node class. The class defines its ports,
menu, and behavior. The node name identifies that instance in links, secrets,
logs, and parts of stored configuration. Almagest requires each node name to be
unique in the graph.

A node can show its state and other runtime information in these parts of the
graph interface:

- The node label is the short text that is always visible inside the node. It
  can show a state such as `up`, a waiting condition, an error, or a current
  property such as an address.
- The node menu opens in the side panel when you select the node. It contains
  the node settings, actions, and more detailed status information.
- A popup is a message from a node. The graph shows the most recent popup with
  the highest severity. Select the node to see all its popups in the same side
  panel as the node menu.

Connected nodes are not necessarily ready, functional, or communicating. Check
the labels, menus, and popups of the nodes in the complete path.

Some node-menu controls take effect immediately. Other controls are part of a
form and take effect only when you apply the form. This behavior depends on the
node class. A change can close active connections or replace a listener or
interface. Read the node reference page before you change a node that currently
processes traffic.

## Ports and direction

By convention, input ports are usually on the left and output ports are usually
on the right. This layout is not an architectural restriction. Links normally
connect an output to an input of the same type.

The node at the output end of a link is the provider. The node at the input end
is the consumer. This link direction does not necessarily identify the direction
of network traffic. A consumer can create outgoing connections and listeners
through the same Network link. A TUN link carries packets in both directions
between the nodes that it connects.

Some ports accept one link. Others accept multiple links or create numbered
slots as you connect them. A node reference page names these cases.

## Links and runtime state

A link is a typed connection between two ports. Some link types carry scalar
data, such as String or Bool. Other link types connect traffic or service
interfaces, such as Network, TUN, or DNS. Some node classes add more specific
link types. Read [Values and logic](@docs/almagest/logic) for scalar links and
[Network, TUN, DNS, and Object](@docs/almagest/types) for the main traffic and
structured-data links.

The state behind a link can change while the graph topology stays the same.
During a failure, the link can remain visible while its connected nodes cannot
use it. Check all nodes in the path.

Removing a link detaches the consumer. Removing a node also removes its links.
The panel supports undo and redo for graph edits, but an undo does not restore
closed external connections or undo effects outside the graph.

## Roots and complete paths

Nodes that affect the host, listen for traffic, or start work can be graph
roots. A graph does not need one global start node. Several independent paths
can run in one graph.

For a traffic path, follow every required input to a provider. This simple path
uses [DefaultTun](@docs/node/almagest/DefaultTun) to capture host traffic,
[Spoofer](@docs/node/almagest/Spoofer) to convert its packets to Network
operations, and [OutNetwork](@docs/node/almagest/OutNetwork) to send those
operations through the normal host network:

```graph
{
  "Default TUN": {
    "Class": "almagest/DefaultTun",
    "Pos": "{\"x\":61,\"y\":120}",
    "Links": ["Tun -> [Flow Converter] Tun"],
  },
  "Flow Converter": {
    "Class": "almagest/Spoofer",
    "Pos": "{\"x\":268,\"y\":120}",
    "Links": ["Network -> [Outside Network] Network"],
  },
  "Outside Network": {
    "Class": "almagest/OutNetwork",
    "Pos": "{\"x\":475,\"y\":120}",
  },
}
```

This preview explains topology only. Read the DefaultTun page and test a narrow
route before you apply this graph.

## Saved graph format

The regular HuJSON configuration stores nodes by name. `Class` selects the node
class, `Pos` stores its panel position, and `Links` names connected ports.
Lowercase fields belong to the node's settings. The panel normally maintains
this text for you. Read [Configuration, KV, and secrets](@docs/almagest/data)
for the complete regular configuration.

This is the complete configuration from the **Hello Network** example. Read
[Snapshots and examples](@docs/almagest/snapshots) to load the example in the
panel.

```hujson
// Hello Network sends an HTTP request through the host network.
// OutNetwork connects to the host network, and HttpClient sends requests.
// Change the URL and inspect the client output.
{
  "nodes": {
    "Outside Network Gateway": {
      "Class": "almagest/OutNetwork",
      "Pos": "{\"x\":735,\"y\":261}",
    },
    "Http Client": {
      "Class": "almagest/HttpClient",
      "Pos": "{\"x\":596,\"y\":260}",
      "Links": ["Network -> [Outside Network Gateway] Network"],
      "url": "http://example.com",
    },
  },
}
```

The keys under `nodes` are the unique node names. In the saved link text,
`Network` before the arrow is a port on **Http Client**. The bracketed text is
the other node name, and the final `Network` is a port on that node. The arrow
separates the two endpoints in the stored text. It does not show the direction
of network traffic. The lowercase `url` field is an HttpClient setting.

Use the config editor for review and controlled bulk changes. An invalid node
class, port name, link, or node setting usually affects only the related nodes
and links. If a node class is not available, Almagest keeps the node, ports, and
links as a placeholder. A syntax error or an invalid top-level configuration can
prevent the complete configuration from applying.
