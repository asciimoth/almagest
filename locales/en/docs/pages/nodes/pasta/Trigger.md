Starts a flow manually. Press **Trigger** in the node menu, or use the
workspace's manual trigger action for this node.

Each activation sends one trigger event to every consumer connected to the
**Trigger** output. One output can supply multiple consumers, so you can start
several branches with one activation. The node has no trigger input and does not
run automatically when the workspace starts or when a connection is added.

A trigger is an event, not a stored value. It carries only the signal to run; it
does not carry a configurable data value. A consumer that connects later does
not receive an earlier trigger. Requests for a current value are ignored and do
not cause a new trigger. Press the button again when you need another event.
