Outputs `true` when [input 1](@port/left/input%201),
[input 2](@port/left/input%202), or both inputs are `true`. Use it when an
action can start after either of two Boolean conditions becomes true. All three
connectors use Boolean values.

A disconnected input, or a connected input that has not supplied a value, counts
as `false`. The [output](@port/right/output) is `false` only while both inputs
are false. Disconnecting an input changes that input back to `false`. Each input
accepts one connection. The [output](@port/right/output) can supply multiple
consumers.

The node requests the current value when an input connects. It recalculates the
result after each input update or disconnection. When the result changes, it
sends the new value to all connected consumers. A consumer that connects later
receives the current result immediately. A current-value request also returns
the current result. The node label and the read-only **Value** field also show
the current result.
