Converts one Object input to a JSON String. Use it when a String node or an
external service needs the JSON representation of an Object.

## Format options

By default, the node produces compact JSON without unnecessary spaces or line
breaks. Enable **Pretty print** to add line breaks and two-space indentation.

Enable **Omit empty/nil** to remove `null` values recursively. It also removes
objects and arrays that become empty after their contents are removed. In an
array, removed items close the gap. This option does not remove `false`, `0`,
or an empty string. If the complete value is removed, the result is `null`.

The format options take effect as soon as you change them. There is no Apply
step. The selected options are saved with the workspace. The read-only
**Result** field shows the current formatted String. The node label stays
empty so that long JSON text does not fill the graph.

## Input and updates

A disconnected input, or an input that has not supplied an Object, has the
value `null`. Disconnecting the input changes the result back to `null`. The
input accepts one connection.

The node requests the current Object when the input connects. It recalculates
the String after each accepted Object update, input disconnection, or format
option change. When the formatted String changes, it sends the new value to
all connected consumers. It does not send a duplicate update when the result
stays the same.

The output can supply multiple consumers. A new consumer receives the current
String immediately. The node also returns the current String when a connected
consumer requests it.

ObjectToString normally receives JSON-compatible objects. If an Object
contains a value that JSON cannot encode, such as a non-finite number or a map
key that is not a string, the result is `null`. The node does not send a
separate error value.
