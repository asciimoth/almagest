# Time Trigger

`almagest/TimeTrigger` emits transient `pasta/trigger` events from one
right-directed `Trigger` port.

The node menu supports three modes:

- Disabled: no timer worker is started.
- Interval: emits after each positive Go duration in `Every`, such as `2m`,
  `30s`, or `1h30m`.
- Daily: emits once per day at the local process wall-clock time in `At`, using
  `HH:MM` or `HH:MM:SS`.

`Trigger on ready` emits one trigger during each `OnReady` callback, including
workspace restore and pasted nodes once their restored links are available. This
does not start a timer worker by itself.

Timer workers are lazy: the node starts a tracked background worker only when
the saved or applied mode is a valid interval or daily schedule. Timer ticks are
generation-checked before emission, so stale ticks from replaced workers cannot
emit after a config change or node stop. Trigger delivery uses Pasta link
validation, so detach and reattach operations only deliver to currently
connected links.
