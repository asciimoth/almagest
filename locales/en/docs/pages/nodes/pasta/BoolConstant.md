Supplies a Boolean value to one or more consumers. The initial value is `false`
unless the workspace contains a saved value.

Set **Value** in the node menu, and then apply the form. Editing the checkbox
without applying the form does not change the output. After an applied value
changes, the node updates its label and sends the new value to every connected
consumer. Applying the same value again does not send a duplicate update.

A consumer receives the current value as soon as it connects. The node also
returns the current value when a connected consumer requests it. The applied
value is saved with the workspace.
