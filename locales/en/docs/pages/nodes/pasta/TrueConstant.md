TrueConstant supplies the fixed Boolean value `true`. Use it for conditions,
selectors, or other Boolean inputs that must always be true. The node has no
input or editable value, so its value does not change while it is active.

TrueConstant sends `true` to all connected consumers when the graph becomes
ready. A new consumer receives `true` when it connects. The node also returns
`true` when a connected consumer requests the current value. The node label and
the read-only **Value** field both show `true`.
