Supplies a configurable String value to one or more consumers. The default value
is an empty string.

Edit **Value**, and then apply the form. A draft edit does not change the
[output](@port/right/output). Applying a different value updates the node label
and sends the new value to all connected consumers. Spaces are preserved.
Applying the current value again does not send another update.

A consumer receives the current value when it connects. StringConstant also
returns the current value when a connected consumer requests it. The applied
value is preserved when you save the workspace or copy and paste the node.
