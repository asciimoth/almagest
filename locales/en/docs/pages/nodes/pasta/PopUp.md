Adds a popup note to the node when **Trigger** receives an event or when you
trigger PopUp manually. Use it to show information, warnings, or errors from a
trigger sequence.

## Text and level

**Text** and **Lvl** each accept one String connection. PopUp requests the
current String when either connection is added. Later updates replace the
stored value for the next popup. Updating these values does not create a popup
by itself.

Use one of these case-sensitive values for **Lvl**:

- `info` creates an information popup.
- `ward` creates a warning popup.
- `err` creates an error popup.

Use the exact spelling `ward` for a warning.

An empty or disconnected Lvl input uses `info`. Any other value uses `err`.
An empty or disconnected Text input supplies an empty message.

## Triggers and stored values

The Trigger input can accept multiple trigger sources. Each trigger uses the
latest stored Lvl and Text values and appends one popup. PopUp does not replace
or combine earlier popups, so repeated triggers can add duplicate messages.

Disconnecting Lvl resets its stored value to empty, which selects `info`.
Disconnecting Text resets its stored value to an empty message. These changes
do not remove popups that already exist. Disconnecting a Trigger source does
not change the stored Lvl or Text.

PopUp does not save the stored Lvl and Text as its own settings. After a
workspace is restored, connected String sources can supply their current
values again.
