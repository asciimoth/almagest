Outputs `true` only when both **input 1** and **input 2** are `true`. Use it
when an action must require two Boolean conditions. All three connectors use
Boolean values.

A disconnected input, or a connected input that has not supplied a value, counts
as `false`. The output is therefore `false` until both inputs supply `true`.
Disconnecting either input changes that input back to `false`. Each input
accepts one connection. The output can supply multiple consumers.

The node requests the current value when an input connects. It recalculates the
result after each input update or disconnection. When the result changes, it
sends the new value to all connected consumers. A consumer that connects later
receives the current result immediately. A current-value request also returns
the current result. The node label and the read-only **Value** field also show
the current result.
