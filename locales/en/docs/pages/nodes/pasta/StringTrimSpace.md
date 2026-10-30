Removes whitespace from the start and end of a String. Whitespace inside the
String is unchanged.

The node uses the Unicode definition of whitespace. It removes spaces, tabs,
line breaks, non-breaking spaces, and other Unicode whitespace characters at
both ends. A zero-width space is not classified as Unicode whitespace, so it is
not removed. If the [input](@port/left/input) contains only whitespace, the
result is an empty string.

If the [input](@port/left/input) is missing or disconnected, the result is an
empty string. The [input](@port/left/input) accepts one connection.
StringTrimSpace requests the current value when the connection is added and when
the workspace starts.

The node recalculates the result for each [input](@port/left/input) update. It
sends an update only when the trimmed result changes. Thus, changing only the
amount of removed whitespace does not send a new value. A newly connected
consumer receives the current result immediately. The node also returns the
current result when a consumer requests it. The node label and the read-only
**Value** field show the current result.
