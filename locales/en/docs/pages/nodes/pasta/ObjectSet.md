Stages an Object value and writes it to a named variable when triggered. Use the
[Trigger](@port/right/Trigger) output to continue a sequence after the write is
complete.

## Staged value

[Value](@port/left/Value) accepts one Object connection. Each received value
replaces the staged value, but it does not change the named variable yet. If no
value is connected, the node uses the [JSON](https://www.json.org/json-en.html)
value in its menu. The default is `null`, and a saved workspace restores the
last configured or received value. Disconnecting Value resets the staged value
to `null`.

The menu value must be a valid JSON object, array, or `null`. A top-level
string, number, or Boolean is not accepted. Applying invalid JSON restores the
previous menu values and does not change the name or staged value.

Object values are immutable. After ObjectSet receives a value, another node
cannot modify that staged value in place. It must send a replacement Object.

## Trigger order

When the node receives a trigger, or when you trigger it manually, it does these
actions in order:

1. It writes the staged Object to the named Object variable.
1. It sends a trigger to all consumers connected to
   [Trigger](@port/right/Trigger).

This order lets you connect the [Trigger](@port/right/Trigger) output to an
[ObjectGet](@docs/node/pasta/ObjectGet) node with the same name.
[ObjectGet](@docs/node/pasta/ObjectGet) then reads the Object that ObjectSet has
written. [Value](@port/left/Value) updates do not send triggers. The
[Trigger](@port/left/Trigger) input can accept multiple trigger sources, and the
[Trigger](@port/right/Trigger) output can supply multiple consumers.

## Variable name, scope, and copies

Apply **Name** and **Value** in the menu to change the variable name and staged
Object. This action does not write the variable. An empty name becomes `var`.
The node label shows the current variable name.

Object variables are shared by ObjectSet and
[ObjectGet](@docs/node/pasta/ObjectGet) nodes in the same workspace. Names must
match exactly. An Object variable is separate from a Boolean, Integer, Float, or
String variable with the same name. If multiple ObjectSet nodes use one name,
the most recently triggered write is current.

Copying and pasting an ObjectSet preserves its name and staged value. The two
nodes have independent staged values, but they still write the same shared
variable while their names match.

The shared value exists while at least one variable node refers to its name.
Removing or renaming the last such node clears the shared value. A later
[ObjectGet](@docs/node/pasta/ObjectGet) then reads the default, `null`. The
workspace saves each ObjectSet node's name and staged value, but it does not
save the shared value as an independent global setting.
