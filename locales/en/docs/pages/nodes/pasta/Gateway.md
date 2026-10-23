Gateway holds data until it receives [Trigger](@port/left/Trigger) or you
trigger the node manually. A typical use connects a source to
[In](@port/left/In) and a consumer to [Out](@port/right/Out). The data path also
works in the opposite direction.

The first concrete data connection to [In](@port/left/In) or
[Out](@port/right/Out) selects the data type for both ports. Later concrete
connections must use that type. A generic connection does not select a concrete
type, and Gateway does not accept Loop connections. The node has no menu
settings.

Gateway stores the latest data value for each side and connection type. It does
not forward a stored value until triggered. For each trigger, it first sends
values received through **In** to **Out**. It then sends values received through
**Out** to **In**. A trigger does not clear the stored values, so a later
trigger sends them again. A newer value replaces the earlier value.

A request for the current value passes to the other side immediately; it does
not wait for **Trigger**. Gateway does not answer the request from its stored
value. A value returned in response is stored and remains blocked until the next
trigger.
