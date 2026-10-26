FloatGet reads a named floating-point variable when it receives
[Trigger](@port/left/Trigger) or when you trigger the node manually. Set
**Name** to the variable to read. If **Name** is empty, the node uses `var`.

For each trigger, FloatGet reads the current value, sends it through
[Value](@port/right/Value), and then sends [Trigger](@port/right/Trigger). This
order lets the next triggered nodes use the newly read value.

FloatGet and [FloatSet](@docs/node/pasta/FloatSet) nodes with the same **Name**
share the variable in the current workspace. Variables of other types are
separate, even if they have the same name. If no
[FloatSet](@docs/node/pasta/FloatSet) node has stored a value, FloatGet reads
`0`.

FloatGet does not monitor the variable. Trigger it again to read a later value.
A new [Value](@port/right/Value) connection receives nothing until the next
trigger. If a consumer requests the current value, FloatGet repeats the last
value sent on that connection. It does not read the variable again. Changing
**Name** clears these remembered values.
