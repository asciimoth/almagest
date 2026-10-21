Supplies a configurable Object value to one or more consumers. The default
value is `null`.

## Accepted JSON

**Value** must contain valid JSON with one of these top-level forms:

- an object, such as `{"name": "Ada"}`
- an array, such as `[1, true, null]`
- `null`

Nested values can contain objects, arrays, strings, numbers, Booleans, and
`null`. A top-level string, number, or Boolean is not accepted.

Edit **Value**, and then apply the form. A draft edit does not change the output.
If the JSON is invalid or has an unsupported top-level form, the node rejects
the edit, restores the previous text in the menu, and keeps supplying the
previous value. A valid apply formats the menu value and sends it to all
connected consumers. This send also occurs when the valid value is equal to
the current value.

## Copies and current value

Object values are immutable. A consumer cannot change the stored constant by
changing a received value. It must create and send a replacement Object.

Copying and pasting an ObjectConstant preserves its current applied value.
The pasted node is independent, so a later edit to one node does not change
the other. The applied value is also saved with the workspace.

A consumer receives the current value as soon as it connects. ObjectConstant
also returns the current value when a connected consumer requests it.
