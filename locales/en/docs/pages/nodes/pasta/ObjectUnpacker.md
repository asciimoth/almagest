Reads selected values from one Object and supplies each value through a typed
output. Add an entry to **Outputs** for each value that you need.

Each output has a name, type, path, and default value. The supported types are
String, Integer, Float, Boolean, and Object. The node creates and removes its
outputs when you apply changes. Changing an output type removes connections that
do not support the new type. Output names are cleaned and made unique if
necessary.

## Paths and types

A path is a non-empty [JSON](https://www.json.org/json-en.html) array. A string
selects a Map key, and a non-negative integer selects a Vector position. For
example:

- `["user","name"]` selects the `name` key in the `user` Map.
- `["items",0]` selects the first item in the `items` Vector.

Each step must match the Object structure. Empty keys, negative indices,
fractional indices, and an empty path are not accepted.

The value at the path must have the selected type. Object accepts a Map or
Vector. The node does not convert values between types. For example, an Integer
value does not satisfy a Float output.

## Missing values and defaults

The node supplies the configured default if the path is missing, contains
`null`, passes through the wrong container type, or ends at a value of the wrong
type. Configure a separate default for each String, Integer, Float, or Boolean
output. An Object output always uses `null` as its default.

If the [input](@port/left/input) is not connected, all outputs supply their
defaults. Disconnecting the [input](@port/left/input) also resets all outputs to
their defaults.

## Updates and validation

ObjectUnpacker requests the current Object when its [input](@port/left/input)
connects. It checks all paths when the Object or output configuration changes.
If at least one result changed, it sends the current values from all outputs. It
sends nothing when all results stay equal. A newly connected consumer receives
the current value immediately. A current-value request also returns that
output's current value.

If any output has an invalid type or path, the node rejects the complete menu
change and keeps the previous outputs. Output definitions and their order are
preserved when you save the workspace or copy and paste the graph.
