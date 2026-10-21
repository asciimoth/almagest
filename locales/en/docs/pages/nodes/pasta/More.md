Outputs `true` when **input 1** is greater than **input 2**. Each input accepts
one connection. The Boolean output can supply multiple consumers.

More supports these standard values:

- Integers and floating-point numbers can be compared with each other. The
  comparison converts both values to floating-point numbers. For example,
  `3 > 2.5` is `true`.
- Strings can be compared with other strings. The comparison is
  case-sensitive and uses lexicographic byte order. It does not convert
  numeric text to a number. For example, `"10" > "2"` is `false`.
- A number and a string are not comparable, so the result is `false`.

A disconnected input, or a connected input that has not supplied a supported
value, has the integer value `0`. This means a missing input does not always
make the result false. For example, input 1 is greater than a missing input 2
when input 1 is a positive number.

Boolean, Object, and other unsupported input values are ignored. An ignored
update does not replace the last accepted value. Disconnecting an input does
replace its stored value with `0`.

The node requests the current value when an input connects. It recalculates
after each accepted update or disconnection. It sends an update to existing
consumers only when the Boolean result changes. A consumer that connects
later, or requests the current value, receives the current result. The node
label and the read-only **Value** field show the same result.
