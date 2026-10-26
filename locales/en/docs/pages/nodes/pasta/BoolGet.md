BoolGet reads a named Boolean variable when it receives
[Trigger](@port/left/Trigger) or when you trigger the node manually. Set
**Name** to the variable to read. If **Name** is empty, the node uses `var`.

For each trigger, BoolGet reads the current value, sends it through
[Value](@port/right/Value), and then sends [Trigger](@port/right/Trigger). This
order lets the next triggered nodes use the newly read value.

BoolGet and [BoolSet](@docs/node/pasta/BoolSet) nodes with the same **Name**
share the variable in the current workspace. Variables of other types are
separate, even if they have the same name. If no
[BoolSet](@docs/node/pasta/BoolSet) node has stored a value, BoolGet reads
`false`.

BoolGet does not monitor the variable. Trigger it again to read a later value. A
new [Value](@port/right/Value) connection receives nothing until the next
trigger. If a consumer requests the current value, BoolGet repeats the last
value sent on that connection. It does not read the variable again. Changing
**Name** clears these remembered values.
