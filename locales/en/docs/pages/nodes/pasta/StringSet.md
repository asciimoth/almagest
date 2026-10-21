Stages a String value and writes it to a named variable when triggered. Use the
**Trigger** output to continue a sequence after the write is complete.

## Staged value

**Value** accepts one String connection. Each received value replaces the staged
value, but it does not change the named variable. If no value is connected, the
node uses **Value** from its menu. The default is an empty string. Disconnecting
the Value connection resets the staged value to an empty string.

Apply **Name** and **Value** in the menu to change the variable name and staged
value. This action does not write the variable or send a trigger. An empty name
becomes `var`. Names must match exactly, and the node label shows the current
name.

## Trigger order

When StringSet receives a trigger, or when you trigger it manually, it does
these actions in order:

1. It writes the staged value to the named String variable.
1. It sends a trigger to all consumers connected to **Trigger**.

This order lets you connect the Trigger output to a
[StringGet](@docs/node/pasta/StringGet) node with the same name.
[StringGet](@docs/node/pasta/StringGet) then reads the value that StringSet has
written. Value updates do not send triggers. The Trigger input can accept
multiple trigger sources, and the Trigger output can supply multiple consumers.

## Scope and persistence

String variables are shared by StringSet and
[StringGet](@docs/node/pasta/StringGet) nodes in the same workspace. They are
separate from Boolean, integer, float, and Object variables with the same name.
If multiple StringSet nodes use one name, the most recently triggered write is
current.

The shared value exists while at least one variable node refers to its name.
Removing or renaming the last such node clears the value. A later
[StringGet](@docs/node/pasta/StringGet) then reads an empty string.

The workspace saves each StringSet node's name and last configured or received
staged value. Restore does not write that value automatically. Trigger the
restored node to write the saved value again. Copying and pasting preserves the
name and staged value. Each copy stages values independently, but matching names
still refer to the same shared variable.
