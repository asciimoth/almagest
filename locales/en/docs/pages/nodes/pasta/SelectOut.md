SelectOut connects [In](@port/left/In) to one of two branches. The Boolean
**Selector** value chooses the active branch:

- `false` connects [In](@port/left/In) with [Out 0](@port/right/Out%200).
- `true` connects [In](@port/left/In) with [Out 1](@port/right/Out%201).

The initial selector value is `false`. The node stores the latest selector
value. If you disconnect **Selector**, the latest value stays in effect. The
node label shows the active branch.

For normal data, the active path is bidirectional. An event from **In** goes to
all connections on the active output. An event from the active output goes to
the connection on **In**. The node ignores events from the inactive output.

Current-value requests follow the same active path. The node does not store a
data value and does not answer these requests itself. When the selector changes,
the node requests current values from both ends of the new path.

The first concrete data connection to **In**, **Out 0**, or **Out 1** selects
one shared data type for these ports. Later concrete connections must use this
type. A generic connection does not select the type. The selected type remains
after connections are removed. Loop connections are not supported.

Trigger data is an exception. A trigger from **In** goes to the active output,
but triggers do not travel from an output to **In**. Current-value requests do
not apply to trigger data.
