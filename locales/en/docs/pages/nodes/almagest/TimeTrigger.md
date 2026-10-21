Sends a **Trigger** on a time schedule. Connect **Trigger** to each node that
must run. One tick sends one Trigger to every currently connected node; it does
not include a timestamp or other value.

The default mode is **Disabled**. The default interval text is `2m`, the default
daily time is `00:00`, and **Trigger on ready** is disabled.

## Interval mode

**Interval** mode waits for the duration in **Every**, sends a Trigger, and
starts the next wait. It does not send a Trigger immediately when you enable the
schedule.

Use a positive duration such as:

- `30s`
- `2m`
- `1h30m`
- `500ms`

Changing the schedule restarts the wait from the time that you apply the change.
A zero or negative duration is invalid.

## Daily mode

**Daily** mode sends a Trigger at the time in **At**. Use 24-hour `HH:MM` or
`HH:MM:SS` format, such as `08:30` or `23:15:20`. Values such as `24:00` are
invalid.

The node uses the local timezone of the daemon process. It has no separate
timezone setting. Check the daemon host or container timezone when the expected
time is different from the observed time.

The next scheduled time is always in the future. If the node starts exactly at
the configured time, the next Trigger is 24 hours later. The daily calculation
uses 24-hour steps, so a daylight-saving transition can shift the local
wall-clock time.

## Startup and manual triggers

Enable **Trigger on ready** to send one Trigger when the node becomes ready,
typically when the workspace starts or is restored. This startup Trigger is
independent of the selected mode and also works when the schedule is disabled.

Enabling **Trigger on ready** in an already running workspace does not send an
immediate Trigger. It takes effect the next time the node becomes ready.
Manually triggering the node always sends a Trigger immediately and does not
reset the schedule.

Triggers are not stored. A node that is disconnected at the scheduled time does
not receive that Trigger after it reconnects.

## Validation

Invalid schedule values are not applied. The previous valid schedule continues
to run, and the menu shows the validation error.

The node label is `time trigger` when disabled, `every DURATION` in Interval
mode, and `daily TIME` in Daily mode.
