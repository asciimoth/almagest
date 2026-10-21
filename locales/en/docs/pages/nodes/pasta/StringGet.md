Reads a named String variable when triggered. It does not read the variable
when the node starts, when a consumer connects, or when you change the name.

## Trigger order

For each received trigger, StringGet does these actions in order:

1. It reads the current value of the named String variable.
2. It sends that value to all consumers connected to **Value**.
3. It sends a trigger to all consumers connected to **Trigger**.

This order lets the next nodes use the new value before they process the
Trigger output. Until the first trigger, StringGet has no delivered value. A
value request can repeat the last value that StringGet sent on that connection,
but it does not read the variable again.

## Name, default, and scope

Set **Name** to the variable that you want to read. Names must match exactly.
The default name is `var`, and an empty name becomes `var`. The node label
shows the current name. Changing the name clears previously delivered values,
so trigger the node again to read the new variable.

String variables are shared by StringGet and [StringSet](@docs/node/pasta/StringSet) nodes in the same
workspace. They are separate from Boolean, integer, float, and Object
variables with the same name. A String variable has an empty string as its
default until a [StringSet](@docs/node/pasta/StringSet) node writes it.

The shared value exists while at least one variable node refers to its name.
Removing or renaming the last such node clears the value. Saving or copying a
StringGet preserves its name, but the current shared value is not stored as an
independent workspace setting.
