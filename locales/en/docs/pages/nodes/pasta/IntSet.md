IntSet stages an integer for a named variable. Supply the integer through
**Value**, or set **Value** in the node menu. The initial staged value is `0`.
Receiving a new value changes the staged value but does not change the shared
variable. Removing the **Value** connection resets the staged value to `0`.

IntSet writes the staged value only when it receives **Trigger** or when you
trigger the node manually. For each trigger, it first updates the variable and
then sends **Trigger**. An IntGet node triggered next can therefore read
the new value.

Set **Name** to the variable to update. If **Name** is empty, the node uses
`var`. IntGet and IntSet nodes with the same **Name** share the
variable in the current workspace. Variables of other types are separate,
even if they have the same name.

The node label shows **Name**, not the staged value. The name and staged value
are saved with the workspace.
