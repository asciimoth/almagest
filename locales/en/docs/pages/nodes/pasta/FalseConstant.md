FalseConstant supplies the fixed Boolean value `false`. Use it for conditions,
selectors, or other Boolean inputs that must always be false. The node has no
input or editable value, so its value does not change while it is active.

FalseConstant sends `false` to all connected consumers when the graph becomes
ready. A new consumer receives `false` when it connects. The node also returns
`false` when a connected consumer requests the current value. The node label
and the read-only **Value** field both show `false`.
