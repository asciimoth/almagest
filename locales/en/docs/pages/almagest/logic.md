Scalar nodes let control values travel through the same graph as network
resources. Current scalar types include String, Int, Float, and Bool. Object
adds JSON-compatible structured values.

> [!NOTE]
> Graph logic is work in progress. The intended result is conditional network
> behavior such as “use this traffic path on the local network, and use another
> path elsewhere.” Some building blocks exist, but complete detection and policy
> workflows can still need external input or manual control.

## Constants and variables

Constant nodes provide a value configured in their menu. Examples include
[StringConstant](@docs/node/pasta/StringConstant),
[IntConstant](@docs/node/pasta/IntConstant), and
[BoolConstant](@docs/node/pasta/BoolConstant).

Get and Set nodes provide named graph variables. A Set node stores a value, and
a Get node with the same variable name reads it. Variables are separate for each
type. A String variable and an Int variable can have the same name without
sharing a value.

Use variables for graph state, not for credentials. Store credentials in
[Secrets](@docs/almagest/data).

## Operations

String nodes can join, format, split, trim, change case, and test content.
Numeric nodes can add, subtract, multiply, divide, and compare values. Object
nodes can pack fields, unpack fields, and convert an Object to JSON text.

An operation runs when it receives the inputs that its node requires. Read the
node page for rules about missing inputs, division, type selection, and retained
values.

This graph adds two integers and tests if the result is more than a limit. The
preview labels show the result for the configured values. In a live graph,
change a constant in its node menu to see the result update through the graph.

```graph
{
  "Base value": {
    "Class": "pasta/IntConstant",
    "Pos": "{\"x\":31,\"y\":53}",
    "Label": "10",
    "Links": ["output -> [Total] input 1"],
    "value": 10,
  },
  "Offset": {
    "Class": "pasta/IntConstant",
    "Pos": "{\"x\":31,\"y\":147}",
    "Label": "5",
    "Links": ["output -> [Total] input 2"],
    "value": 5,
  },
  "Total": {
    "Class": "pasta/Sum",
    "Primary": "pasta/int",
    "Pos": "{\"x\":230,\"y\":100}",
    "Label": "15",
    "Links": ["output -> [Over limit] input 1"],
  },
  "Limit": {
    "Class": "pasta/IntConstant",
    "Pos": "{\"x\":230,\"y\":213}",
    "Label": "12",
    "Links": ["output -> [Over limit] input 2"],
    "value": 12,
  },
  "Over limit": {
    "Class": "pasta/More",
    "Pos": "{\"x\":437,\"y\":133}",
    "Label": "true",
  },
}
```

An Object can supply more than one consumer. In this graph,
[Length](@docs/node/pasta/Length) reports the number of top-level fields.
[ObjectToString](@docs/node/pasta/ObjectToString) supplies the same Object as
JSON text. A firewall, command, or other node can consume the Object instead.

```graph
{
  "Policy data": {
    "Class": "pasta/ObjectConstant",
    "Pos": "{\"x\":46,\"y\":113}",
    "Links": [
      "output -> [Field count] input",
      "output -> [JSON text] input",
    ],
    "value": {"action": "allow", "priority": 10},
  },
  "Field count": {
    "Class": "pasta/Length",
    "Pos": "{\"x\":299,\"y\":60}",
    "Label": "2",
  },
  "JSON text": {
    "Class": "pasta/ObjectToString",
    "Pos": "{\"x\":299,\"y\":167}",
    "Label": "{\"action\":\"allow\",\"priority\":10}",
  },
}
```

## Boolean control and selection

Bool values can come from constants, variables, comparisons, and Boolean
operations. [Select](@docs/node/pasta/Select) chooses one of two input branches.
[SelectOut](@docs/node/pasta/SelectOut) chooses one of two output branches. The
first concrete data link selects their shared data type, so the same selector
can switch a Network, String, Object, or another supported type.

A Select changes which value or live resource is connected. Existing network
connections can close when a selected Network or Tun path changes. A Boolean
selection is control logic, not a transaction across the complete graph.

This graph connects one of two [HttpServer](@docs/node/almagest/HttpServer)
nodes to an isolated [MockNetwork](@docs/node/almagest/MockNetwork). An
[HttpClient](@docs/node/almagest/HttpClient) also uses that network. `false`
connects **Server A**, and `true` connects **Server B**. In a live graph, send a
request before and after you change **Select server** to see the response
change. The server that is not selected has no path to the client.

```graph
{
  "Select server": {
    "Class": "pasta/BoolConstant",
    "Pos": "{\"x\":230,\"y\":187}",
    "Label": "false",
    "Links": ["output -> [Active server] Selector"],
    "value": false,
  },
  "Response A": {
    "Class": "pasta/StringConstant",
    "Pos": "{\"x\":31,\"y\":27}",
    "Label": "Response from server A",
    "Links": ["output -> [Server A] Response"],
    "value": "Response from server A",
  },
  "Server A": {
    "Class": "almagest/HttpServer",
    "Pos": "{\"x\":230,\"y\":40}",
    "Links": ["Network -> [Active server] In 0"],
  },
  "Response B": {
    "Class": "pasta/StringConstant",
    "Pos": "{\"x\":31,\"y\":333}",
    "Label": "Response from server B",
    "Links": ["output -> [Server B] Response"],
    "value": "Response from server B",
  },
  "Server B": {
    "Class": "almagest/HttpServer",
    "Pos": "{\"x\":230,\"y\":320}",
    "Links": ["Network -> [Active server] In 1"],
  },
  "Active server": {
    "Class": "pasta/Select",
    "Primary": "almagest/network",
    "Pos": "{\"x\":452,\"y\":180}",
    "Label": "0",
    "Links": ["Out -> [Test network] Network"],
  },
  "HTTP client": {
    "Class": "almagest/HttpClient",
    "Pos": "{\"x\":452,\"y\":40}",
    "Links": ["Network -> [Test network] Network"],
  },
  "Test network": {
    "Class": "almagest/MockNetwork",
    "Pos": "{\"x\":675,\"y\":133}",
  },
}
```

## Triggers, gates, and loops

[Trigger](@docs/node/pasta/Trigger) emits an event.
[Gateway](@docs/node/pasta/Gateway) holds the latest value until it receives a
trigger. Loop nodes can repeat trigger-controlled work and expose iteration
state.

This graph holds the latest input string at Gateway. In a live graph built with
this pattern, select **Trigger** on **Release value** to let the current string
pass to StringUpper. A later input change stays at Gateway until the next
trigger.

```graph
{
  "Input value": {
    "Class": "pasta/StringConstant",
    "Pos": "{\"x\":31,\"y\":67}",
    "Label": "ready",
    "Links": ["output -> [Release gate] In"],
    "value": "ready",
  },
  "Release value": {
    "Class": "pasta/Trigger",
    "Pos": "{\"x\":31,\"y\":187}",
    "Links": ["Trigger -> [Release gate] Trigger"],
  },
  "Release gate": {
    "Class": "pasta/Gateway",
    "Primary": "pasta/string",
    "Pos": "{\"x\":261,\"y\":120}",
    "Links": ["Out -> [Released value] input"],
  },
  "Released value": {
    "Class": "pasta/StringUpper",
    "Pos": "{\"x\":491,\"y\":120}",
    "Label": "READY",
  },
}
```

Avoid fast loops with no external wait or clear termination condition. They can
consume CPU and produce large log or state updates. Build and test control logic
with MockNetwork before it selects a host traffic path.
