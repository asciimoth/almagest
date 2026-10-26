Less compares [input 1](@port/left/input%201) with
[input 2](@port/left/input%202) and outputs `true` when the first value is less
than the second value.

The supported comparisons are:

- Integer with Integer, Float with Float, or a mixed Integer and Float pair.
- String with String, in lexicographic order.

A numeric value cannot be compared with a String. Such a comparison is `false`.
Each missing input has the numeric value `0`. For example, a missing
[input 1](@port/left/input%201) is less than a numeric
[input 2](@port/left/input%202) of `5`. A value of an unsupported type is
ignored; the input keeps its last supported value, or `0` if it has not received
one.

Less requests the current value when an input connects. It sends the current
result when a consumer connects, when the result changes, and when a consumer
requests it. It does not send another update when an input changes but the
comparison result stays the same. The node label and the read-only **Value**
field show the current result.
