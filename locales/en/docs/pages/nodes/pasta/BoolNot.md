Outputs the opposite of [input 1](@port/left/input%201). A `true` input produces
`false`, and a `false` input produces `true`.

A disconnected input, or a connected input that has not supplied a value, counts
as `false`. The [output](@port/right/output) is therefore `true` until the input
supplies `true`. Disconnecting the input changes it back to `false`, so the
[output](@port/right/output) changes back to `true`. The input accepts one
connection. The [output](@port/right/output) can supply multiple consumers.

The node requests the current value when the input connects. It recalculates the
result after each input update or disconnection. When the result changes, it
sends the new value to all connected consumers. It does not send a duplicate
update when the result stays the same. A consumer that connects later receives
the current result immediately. A current-value request also returns the current
result. The node label and the read-only **Value** field also show the current
result.
