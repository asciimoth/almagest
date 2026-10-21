Divides **input 1** by **input 2**. For example, inputs `9` and `3` produce `3`.

The first numeric input that you connect selects the result type. An Integer
input selects integer division, and a Float input selects floating-point
division. The other input is converted to the selected type. Integer division
removes the fractional part toward zero. If Integer is selected, conversion from
Float also removes the fractional part toward zero. The selected type does not
change when you disconnect an input.

A disconnected input, or a connected input that has not supplied a value, counts
as zero. A zero value for **input 2** always produces zero instead of an error
or an infinite result. Each input accepts one connection. The output can supply
multiple consumers.

The node requests the current value when an input connects. It recalculates the
result after each input update or disconnection. When the result changes, it
sends the new value to all connected consumers. It does not send a duplicate
update when the result stays the same. After the result type is selected, a
consumer that connects later receives the current result immediately. The node
label and the read-only **Value** field also show the current result.
