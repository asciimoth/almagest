Stages a Boolean value and writes it to a named variable when triggered. Use
the **Trigger** output to continue a sequence after the write is complete.

## Staged value

**Value** accepts one Boolean connection. Each received value replaces the
staged value, but it does not change the named variable yet. If no value is
connected, the node uses the value in its menu. The default is `false`, and a
saved workspace restores the last configured or received value. Disconnecting
the Value connection resets the staged value to `false`.

You can also edit **Name** and **Value** in the menu. Applying these fields
changes the variable name and staged value. It does not write the variable.
An empty name becomes `var`. The node label shows the current variable name.

## Trigger order

When the node receives a trigger, or when you trigger it manually, it does
these actions in order:

1. It writes the staged value to the named Boolean variable.
2. It sends a trigger to all consumers connected to **Trigger**.

This order lets you connect the Trigger output to a [BoolGet](@docs/node/pasta/BoolGet) node with the same
name. [BoolGet](@docs/node/pasta/BoolGet) then reads the value that BoolSet has written. Value updates
do not send triggers. The Trigger input can accept multiple trigger sources,
and the Trigger output can supply multiple consumers.

## Variable scope

Boolean variables are shared by BoolSet and [BoolGet](@docs/node/pasta/BoolGet) nodes in the same
workspace. Names must match exactly. A Boolean variable is separate from an
Integer, Float, String, or Object variable with the same name. If multiple
BoolSet nodes use one name, the most recently triggered write is the current
value.

The shared value exists while at least one variable node refers to its name.
Removing or renaming the last such node clears the shared value. A later
[BoolGet](@docs/node/pasta/BoolGet) then reads the Boolean default, `false`. The workspace saves each
BoolSet node's name and staged value, but it does not save the shared value as
an independent global setting.
