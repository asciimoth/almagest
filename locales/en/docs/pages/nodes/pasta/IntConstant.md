Supplies one whole-number Integer value to one or more consumers. Use it for
values that must not contain a fractional part, such as a count or loop step.
The initial value is `0` unless the workspace contains a saved value.

Set **Value** in the node menu, and then apply the form. Editing the field
without applying the form does not change the output. If an applied value is
not a valid integer, the current value stays unchanged. After an applied value
changes, the node updates its label and sends the new value to every connected
consumer. Applying the same value again does not send a duplicate update.

A consumer receives the current value as soon as it connects. The node also
returns the current value when a connected consumer requests it. The applied
value is saved with the workspace.
