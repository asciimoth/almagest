Select connects one of two branches to [Out](@port/right/Out). The Boolean
**Selector** value chooses the branch:

- `false` connects [In 0](@port/left/In%200) with [Out](@port/right/Out).
- `true` connects [In 1](@port/left/In%201) with [Out](@port/right/Out).

The initial selector value is `false`. Select keeps the latest Boolean value;
disconnecting **Selector** does not reset it. The node label and the read-only
**Selector** field show the active branch.

For normal data, events from the active input pass to **Out**, and events from
**Out** pass back to the active input. Events from the inactive input are
ignored and are not saved for later. Current-value requests use the same active
path. Select does not cache a value or answer a request itself.

When the selector changes, Select immediately requests current values from the
new active input and from **Out**. It also requests values when an active path
connection is added. This lets the newly selected path update without waiting
for its next normal change.

The first concrete data connection to **In 0**, **In 1**, or **Out** selects one
shared data type for all three ports. Later concrete connections must use that
type. A generic connection does not select a type, and Select does not accept
Loop connections. The selected type remains after connections are removed.

Trigger data is an exception: Select forwards triggers only from the active
input to **Out**. It does not send current-value requests, replay a previous
trigger, or send triggers from **Out** back to an input.
