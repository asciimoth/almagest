Outputs `true` when [input 1](@port/left/input%201) equals
[input 2](@port/left/input%202). The [output](@port/right/output) is always a
Boolean value.

Use Integer, Float, or String inputs. Integer and Float values can compare with
each other, so Integer `3` equals Float `3.0`. Numeric comparison converts
values to floating-point numbers and uses exact equality. It does not use a
tolerance. A String compares only with another String and the comparison is
case-sensitive. A String and a number are not equal. Values of unsupported types
are ignored, and the last accepted value for that input stays in use.

A disconnected input, or a connected input that has not supplied an accepted
value, counts as Integer `0`. Both missing inputs therefore produce `true`. A
missing input also equals numeric zero, but it does not equal an empty String.
Disconnecting an input changes it back to Integer `0`. Each input accepts one
connection. The [output](@port/right/output) can supply multiple consumers.

The node requests the current value when an input connects. It recalculates the
result after each accepted input update or disconnection. When the result
changes, it sends the new value to all connected consumers. It does not send a
duplicate update when the result stays the same. A consumer that connects later
receives the current result immediately. A current-value request also returns
the current result. The node label and the read-only **Value** field also show
the current result.
