StringLength outputs the number of bytes in [input 1](@port/left/input%201). It
does not count Unicode characters or displayed symbols. ASCII characters use one
byte, but other characters can use more. For example, `abc` has length `3`,
while the UTF-8 text `é` has length `2`.

A missing input has the value `""` and length `0`. Removing the input connection
restores this value.

StringLength requests the current string when the input connects. It
recalculates when the string changes or disconnects, and sends an update only
when the byte length changes. A consumer receives the current Integer result
when it connects or requests it. The node label and the read-only **Value**
field show the result.
