StringConcat joins all input strings in numbered order, from
[input 1](@port/left/input%201) to the highest numbered input. It does not
insert a separator. Connect a String that contains the required spaces, commas,
or other separators.

The node starts with [input 1](@port/left/input%201). When all input slots are
connected, it adds the next numbered slot. When you remove connections, it
removes unused trailing slots until one free slot remains. Each slot accepts one
connection. An unconnected slot, or a connected slot that has not supplied a
value, is an empty string and does not change the result.

StringConcat requests the current value when an input connects. It recalculates
when an input value changes or a connection is removed, and sends an update only
when the joined string changes. A consumer receives the current string when it
connects or requests it. The node label and the read-only **Value** field show
the current result.
