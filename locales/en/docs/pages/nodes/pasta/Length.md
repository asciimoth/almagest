Measures the top-level size of one **Object** value and supplies the result as
an integer. The input accepts one connection. The output can supply multiple
consumers.

Length uses these rules:

- A JSON object returns its number of top-level members.
- A JSON array returns its number of top-level elements.
- A top-level string returns its byte length.
- `null`, numbers, Booleans, and unsupported values return `0`.

The node does not count nested members or elements. For example,
`{"items": [1, 2, 3]}` has a length of `1`, not `3` or `4`.

String length counts bytes, not Unicode characters. For example, the UTF-8
string `é` has a length of `2`. Standard Object editors accept an object, an
array, or `null`, but Length can also measure a top-level string supplied by a
compatible Object source.

The result starts at `0`. It also returns to `0` when the input disconnects.
When an input connects, Length requests its current value. Each later input
update recalculates the result. The node sends an update to existing consumers
only when the numeric result changes, so two different values with the same
length do not cause a second output event. A consumer that connects later, or
requests the current value, receives the current result. The node label and the
read-only **Value** field show the same result.
