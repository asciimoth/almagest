Stages a floating-point value and writes it to a named variable when triggered.
Use the [Trigger](@port/right/Trigger) output to continue a sequence after the
write is complete.

## Staged value

[Value](@port/left/Value) accepts one Float connection. Each received value
replaces the staged value, but it does not change the named variable yet. If no
value is connected, the node uses the value in its menu. The default is `0`, and
a saved workspace restores the last configured or received value. Disconnecting
the Value connection resets the staged value to `0`.

You can also edit **Name** and **Value** in the menu. Applying these fields
changes the variable name and staged value. It does not write the variable. An
empty name becomes `var`. Names are case-sensitive. The node label shows the
current variable name.

## Trigger order

When the node receives a trigger, or when you trigger it manually, it does these
actions in order:

1. It writes the staged value to the named Float variable.
1. It sends a trigger to all consumers connected to
   [Trigger](@port/right/Trigger).

This order lets you connect the [Trigger](@port/right/Trigger) output to a
[FloatGet](@docs/node/pasta/FloatGet) node with the same name.
[FloatGet](@docs/node/pasta/FloatGet) then reads the value that FloatSet has
written. [Value](@port/left/Value) updates do not send triggers. The
[Trigger](@port/left/Trigger) input can accept multiple trigger sources, and the
[Trigger](@port/right/Trigger) output can supply multiple consumers.

## Variable scope

Float variables are shared by FloatSet and [FloatGet](@docs/node/pasta/FloatGet)
nodes in the same workspace. Names must match exactly. A Float variable is
separate from a Boolean, Integer, String, or Object variable with the same name.
If multiple FloatSet nodes use one name, the most recently triggered write is
the current value. A [FloatGet](@docs/node/pasta/FloatGet) reads `0` until a
FloatSet writes the variable.

The shared value exists while at least one variable node refers to its name.
Removing or renaming the last such node clears the shared value. A later
[FloatGet](@docs/node/pasta/FloatGet) then reads `0`. The workspace saves each
FloatSet node's name and staged value, but it does not save the shared value as
an independent global setting.
