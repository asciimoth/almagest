Subtracts **input 2** from **input 1**. Input order is fixed, so reversing
the connections changes the sign of the result.

## Number type

The first connected value that establishes a numeric type selects Integer or
Float for the node and its output. This selection remains in effect if you
disconnect that input. An existing output connection is removed if it cannot
carry the selected type. The other input is converted to the selected type:

- With an Integer result, a Float input is truncated toward zero before the
  subtraction. For example, `3 - 1.5` gives `2` if Integer was selected first.
- With a Float result, an Integer input is converted to Float. The same values
  then give `1.5` if Float was selected first.

Connect a Float source first when the result must keep fractional values.
Before a type is selected, the node does not supply a numeric result.

## Missing values and updates

Each input accepts one connection. A missing or disconnected input has value
zero. Thus, only **input 1** gives its own value, while only **input 2** gives
zero minus that value.

Sub requests the current values when inputs connect and when the workspace
starts. It recalculates after each input update or disconnection. It sends an
update only when the result changes. A newly connected consumer receives the
current result immediately after the number type is selected. The node also
returns the current result when a consumer requests it.
