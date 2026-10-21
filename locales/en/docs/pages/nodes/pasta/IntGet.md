IntGet reads a named integer variable when it receives **Trigger** or when you
trigger the node manually. Set **Name** to the variable to read. If **Name** is
empty, the node uses `var`.

For each trigger, IntGet reads the current value, sends it through **Value**,
and then sends **Trigger**. This order lets the next triggered nodes use the
newly read value.

IntGet and IntSet nodes with the same **Name** share the variable in
the current workspace. Variables of other types are separate, even if they
have the same name. If no IntSet node has stored a value, IntGet reads
`0`.

IntGet does not monitor the variable. Trigger it again to read a later value.
A new **Value** connection receives nothing until the next trigger. If a
consumer requests the current value, IntGet repeats the last value sent on
that connection. It does not read the variable again. Changing **Name** clears
these remembered values.
